package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const appTitle = "Discord pela Proton"

type options struct {
	channel string
	check   bool
	watch   time.Duration
}

type app struct {
	local   string
	dataDir string
	log     *log.Logger
	turn    *namedMutex
}

func main() {
	// O mutex da fila pertence a thread que o pegou, e o Go troca goroutines de thread.
	// Presa aqui, a goroutine principal pega e solta sempre da mesma.
	runtime.LockOSThread()

	opts := options{}
	flag.StringVar(&opts.channel, "channel", "stable", "qual Discord abrir: stable, ptb ou canary")
	flag.BoolVar(&opts.check, "check", false, "so mostra o que mudaria na lista da Proton, sem mexer em nada")
	flag.DurationVar(&opts.watch, "watch", 3*time.Minute, "quanto tempo vigiar o Discord depois de abrir, para pegar atualizacao instalada na abertura")
	flag.Parse()

	a, err := newApp()
	if err != nil {
		messageBox(err.Error(), mbIconError)
		os.Exit(1)
	}

	if err := a.run(opts); err != nil {
		a.log.Printf("erro: %v", err)
		messageBox(err.Error(), mbIconError)
		os.Exit(1)
	}
}

func newApp() (*app, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return nil, errors.New("LOCALAPPDATA nao esta definido")
	}
	dataDir := filepath.Join(local, "DiscordProton")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}

	logPath := filepath.Join(dataDir, "discordproton.log")
	trimLog(logPath, 1<<20, 256<<10)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	turn, err := openMutex(`Local\DiscordProton`)
	if err != nil {
		return nil, fmt.Errorf("nao consegui criar a fila entre copias: %w", err)
	}
	return &app{local: local, dataDir: dataDir, log: log.New(io.MultiWriter(f, os.Stderr), "", log.LstdFlags), turn: turn}, nil
}

const turnWait = 2 * time.Minute

// Espera a vez em vez de desistir: um clique no atalho de outro Discord durante a
// troca da lista saia calado, e para quem clicou o Discord simplesmente nao abria.
// So segura a vez quem mexe na Proton; a vigia depois de abrir roda sem ela.
func (a *app) takeTurn() (release func(), err error) {
	ok, err := a.turn.acquire(0)
	if err == nil && !ok {
		a.log.Print("outra copia esta mexendo na Proton, esperando a vez")
		ok, err = a.turn.acquire(turnWait)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("outra janela do DiscordProton esta esperando uma resposta sua ha mais de 2 minutos, provavelmente a pergunta para reconectar a Proton. Responda la e clique no atalho de novo")
	}
	return a.turn.release, nil
}

func (a *app) run(opts options) error {
	channel, err := lookupChannel(opts.channel)
	if err != nil {
		return err
	}
	a.log.Printf("--- %s", channel.Friendly)

	installs := detectInstalls(a.local)
	target, ok := installFor(installs, channel)
	if !ok {
		return fmt.Errorf("nao achei o %s instalado (ou ele esta no meio de uma atualizacao). Abra ele uma vez pelo atalho normal e tente de novo", channel.Friendly)
	}
	a.log.Printf("versao atual: %s", target.Exe)

	if opts.check {
		_, list, plan, err := a.plan(installs)
		if err != nil {
			return err
		}
		summary := describePlan(list, plan)
		a.log.Print(summary)
		messageBox(summary, mbIconInfo)
		return nil
	}

	release, err := a.takeTurn()
	if err != nil {
		return err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	// O plano sai depois de pegar a vez: quem estava na frente pode ter acabado de
	// atualizar a lista, e ai nao ha mais nada a fazer.
	settings, list, plan, err := a.plan(installs)
	if err != nil {
		return err
	}

	declined := false
	if plan.changesTunnel() {
		err := a.update(settings, list, plan, installs)
		switch {
		case errors.Is(err, errReconnectDeclined):
			declined = true
		case err != nil:
			return err
		}
	} else {
		if !plan.empty() {
			a.log.Print("so ha versoes antigas para tirar da lista, isso fica para a proxima atualizacao")
		}
		if err := a.ensureConnected(installs); err != nil {
			return err
		}
	}

	if err := launchDiscord(target); err != nil {
		return fmt.Errorf("nao consegui abrir o %s: %w", channel.Friendly, err)
	}
	a.log.Printf("%s aberto", channel.Friendly)

	release()
	release = nil
	if declined {
		return nil
	}
	if err := a.checkRoute(target); err != nil {
		return err
	}
	return a.watch(target, opts.watch)
}

func installFor(installs []Install, channel Channel) (Install, bool) {
	for _, install := range installs {
		if install.Channel.Key == channel.Key {
			return install, true
		}
	}
	return Install{}, false
}

// Todos os canais instalados entram de uma vez: cada atualizacao custa uma reconexao da
// Proton, entao vale resolver os tres na mesma.
func (a *app) plan(installs []Install) (*protonSettings, appList, syncPlan, error) {
	path, err := findProtonSettings(a.local)
	if err != nil {
		return nil, appList{}, syncPlan{}, err
	}
	settings, err := loadProtonSettings(path)
	if err != nil {
		return nil, appList{}, syncPlan{}, err
	}
	if !settings.includeModeOn() {
		return nil, appList{}, syncPlan{}, errors.New("o split tunneling da Proton precisa estar ligado no modo Incluir.\n\nNa Proton: Configuracoes > Split tunneling > ligar, e escolher \"Incluir\" (so os apps escolhidos usam a VPN). Depois rode de novo")
	}
	list, err := settings.appList()
	if err != nil {
		return nil, appList{}, syncPlan{}, err
	}

	var want, roots []string
	for _, install := range installs {
		want = append(want, install.Exe)
		roots = append(roots, install.Root)
	}
	return settings, list, planSync(list.apps, want, roots, fileExists), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func describePlan(list appList, plan syncPlan) string {
	if plan.empty() {
		return "A lista da Proton ja esta em dia. Nada a mudar."
	}
	var b strings.Builder
	b.WriteString("Mudaria na lista da Proton:\n")
	for _, path := range plan.Add {
		b.WriteString("\n+ " + path)
	}
	for _, i := range plan.Activate {
		b.WriteString("\n+ " + list.apps[i].AppFilePath + " (reativado)")
	}
	for _, i := range plan.Drop {
		b.WriteString("\n- " + list.apps[i].AppFilePath + " (versao que ja nao existe)")
	}
	return b.String()
}

// A Proton manda a lista ao servico so na hora de conectar, e nao tem comando para
// reconectar. Por isso: fecha o app, grava a lista, reinicia o servico (que as vezes
// guarda as regras da lista antiga), reabre o app e conecta pela janela dele.
func (a *app) update(settings *protonSettings, list appList, plan syncPlan, installs []Install) error {
	a.log.Print(describePlan(list, plan))

	text, err := list.apply(plan)
	if err != nil {
		return err
	}
	if err := settings.setStr(keyAppsList, text); err != nil {
		return err
	}

	if !confirmReconnect() {
		a.log.Print("reconexao recusada, a lista da Proton fica como estava")
		return errReconnectDeclined
	}
	a.log.Print("reconexao confirmada")

	wasUp := tunnelUp()
	outside := a.discordsOutside(installs, list)
	if !wasUp {
		outside = a.discordsOutside(installs, appList{})
	}
	// Com o servico reiniciado o tunel cai, e todo Discord aberto reconecta por casa.
	everyone := a.discordsOutside(installs, appList{})

	if protonClientRunning() {
		a.log.Print("fechando o app da Proton para ele reler a configuracao")
		if err := stopProtonClient(); err != nil {
			return err
		}
	}
	backup, err := settings.save(filepath.Join(a.dataDir, "backup"))
	if err != nil {
		return fmt.Errorf("nao consegui gravar a lista da Proton: %w", err)
	}
	a.log.Printf("lista gravada, backup em %s", backup)

	action := "connect"
	if err := restartProtonService(); err != nil {
		// Sem o reinicio, sobra a reconexao de antes; a conferencia da rota depois de
		// abrir avisa se a Proton ficou com a lista antiga.
		a.log.Printf("servico da Proton nao reiniciado (%v), so reconectando", err)
		if wasUp {
			action = "reconnect"
		}
	} else {
		a.log.Print("servico da Proton reiniciado")
		outside = everyone
	}

	if err := showProtonClient(); err != nil {
		return fmt.Errorf("nao consegui reabrir a Proton: %w", err)
	}
	if err := a.connect(action); err != nil {
		return err
	}
	return a.restart(outside)
}

var errReconnectDeclined = errors.New("reconexao da Proton recusada")

// Nos segundos em que a Proton reconecta, conexao nova de qualquer programa pode nascer
// presa no tunel, e a que ja estava nele cai. Foi assim que um jogo aberto acabou indo
// pela VPN. Por isso a reconexao so acontece com o sim de quem esta no computador.
func confirmReconnect() bool {
	text := "O Discord foi atualizado e a Proton precisa reiniciar para incluir a versão nova (uns 15 segundos).\n\n" +
		"Em seguida o Windows pede permissão de administrador: é para reiniciar o serviço da Proton, que às vezes continua usando a lista antiga. Os Discords abertos reiniciam junto.\n\n" +
		"Se tiver um jogo aberto, feche antes ou espere a partida acabar: o que se conectar durante a reconexão pode acabar indo pela VPN.\n\n" +
		"OK: reiniciar agora.\nCancelar: abrir o Discord assim mesmo, sem VPN (o Go Live fica bloqueado até a próxima vez)."
	return messageBox(text, mbOKCancel|mbIconWarning) == idOK
}

const routeWait = 45 * time.Second

// Confere pelo IP local das conexoes que o Discord saiu mesmo pelo tunel. Sem isso, o
// servico da Proton com as regras da lista antiga passava calado: o programa dizia que
// estava tudo certo e o Go Live continuava bloqueado.
func (a *app) checkRoute(target Install) error {
	name := target.Channel.Friendly
	switch waitRoute(target.Channel.Binary, routeWait) {
	case routeTunnel:
		a.log.Printf("%s saindo pelo tunel", name)
		return nil
	case routeUnknown:
		a.log.Printf("%s sem conexao com a internet para conferir por onde sai", name)
		return nil
	}

	// Discord que se atualizou na propria abertura sai de uma pasta que ainda nao esta na
	// lista. Ai o conserto e a lista nova, e quem cuida disso e a vigia.
	if a.offList(target) {
		a.log.Printf("%s saindo por fora do tunel de uma pasta que ainda nao esta na lista", name)
		return nil
	}
	a.log.Printf("%s saindo por fora do tunel, mesmo estando na lista da Proton", name)
	if !confirmRepair(name) {
		a.log.Print("conserto recusado")
		return nil
	}
	err := a.repairInTurn()
	if errors.Is(err, errElevationDeclined) {
		a.log.Print("sem permissao de administrador, o Discord fica fora da VPN")
		return nil
	}
	if err != nil {
		return err
	}
	if waitRoute(target.Channel.Binary, routeWait) == routeOutside && !a.offList(target) {
		return fmt.Errorf("mesmo com o servico da Proton reiniciado, o %s continua saindo por fora da VPN. Confira na Proton se o split tunneling esta no modo Incluir e se o Discord aparece marcado na lista", name)
	}
	a.log.Printf("%s consertado", name)
	return nil
}

func (a *app) offList(target Install) bool {
	_, list, _, err := a.plan(detectInstalls(a.local))
	return err == nil && len(a.discordsOutside([]Install{target}, list)) > 0
}

func confirmRepair(name string) bool {
	text := fmt.Sprintf("O %s abriu, mas está saindo pela internet de casa, e não pela VPN: o Go Live vai ficar bloqueado.\n\n"+
		"Isso acontece quando o serviço da Proton continua usando a lista antiga. Para consertar, ele precisa reiniciar: o Windows vai pedir permissão de administrador, e os Discords abertos reiniciam junto.\n\n"+
		"Se tiver um jogo aberto, feche antes ou espere a partida acabar.\n\n"+
		"OK: consertar agora.\nCancelar: deixar como está.", name)
	return messageBox(text, mbOKCancel|mbIconWarning) == idOK
}

// Um Discord recem-aberto leva alguns segundos ate a primeira conexao.
func waitRoute(binary string, timeout time.Duration) route {
	deadline := time.Now().Add(timeout)
	for {
		r := currentRoute(binary)
		if r != routeUnknown || time.Now().After(deadline) {
			return r
		}
		time.Sleep(2 * time.Second)
	}
}

func currentRoute(binary string) route {
	tunnel, ok := tunnelIP()
	if !ok {
		return routeUnknown
	}
	procs, _ := processes(binary)
	if len(procs) == 0 {
		return routeUnknown
	}
	pids := make(map[uint32]bool, len(procs))
	for _, p := range procs {
		pids[p.pid] = true
	}
	conns, err := establishedTCP()
	if err != nil {
		return routeUnknown
	}
	return classifyRoute(conns, pids, tunnel)
}

// Reinicia o servico da Proton, conecta de novo e reabre os Discords abertos, que com o
// tunel caido reconectaram por casa.
func (a *app) repairInTurn() error {
	release, err := a.takeTurn()
	if err != nil {
		return err
	}
	defer release()

	everyone := a.discordsOutside(detectInstalls(a.local), appList{})
	if err := restartProtonService(); err != nil {
		return err
	}
	a.log.Print("servico da Proton reiniciado")
	if err := showProtonClient(); err != nil {
		return fmt.Errorf("nao consegui abrir a Proton: %w", err)
	}
	if err := a.connect("connect"); err != nil {
		return err
	}
	return a.restart(everyone)
}

// Discord aberto por um caminho que nao estava ativo na lista saiu pela internet de
// casa e assim continua ate fechar: conexao ja aberta nao muda de rota.
func (a *app) discordsOutside(installs []Install, list appList) []Install {
	var outside []Install
	for _, install := range installs {
		paths, _ := processPaths(install.Channel.Binary)
		for _, path := range paths {
			if !list.active(path) {
				outside = append(outside, install)
				break
			}
		}
	}
	return outside
}

func (a *app) restart(installs []Install) error {
	for _, install := range installs {
		a.log.Printf("reiniciando o %s, que estava fora do tunel", install.Channel.Friendly)
		if err := killDiscord(install.Channel); err != nil {
			return err
		}
	}
	if len(installs) > 0 {
		// O Discord e instancia unica e demora para soltar a trava ao morrer.
		time.Sleep(2 * time.Second)
	}
	for _, install := range installs {
		if err := launchDiscord(install); err != nil {
			return fmt.Errorf("nao consegui reabrir o %s: %w", install.Channel.Friendly, err)
		}
	}
	return nil
}

func (a *app) ensureConnected(installs []Install) error {
	if tunnelUp() {
		return nil
	}
	a.log.Print("a Proton esta desconectada")

	// Sem tunel, todo Discord aberto agora esta saindo por casa.
	outside := a.discordsOutside(installs, appList{})

	if err := showProtonClient(); err != nil {
		return fmt.Errorf("nao consegui abrir a Proton: %w", err)
	}
	if err := a.connect("connect"); err != nil {
		return err
	}
	return a.restart(outside)
}

const connectTimeout = 60 * time.Second

func (a *app) connect(action string) error {
	since := time.Now()
	result, err := protonUI(action, connectTimeout)
	if err != nil {
		a.log.Printf("automacao da Proton falhou (%s): %v", action, err)
		return a.connectByHand(err)
	}
	a.log.Printf("Proton: %s", result.where())

	server, err := waitConnection(a.local, since, connectTimeout)
	if err != nil {
		return a.connectByHand(err)
	}
	return checkServer(server)
}

// Ultimo recurso quando a tela da Proton muda e a automacao nao acha os botoes.
func (a *app) connectByHand(cause error) error {
	since := time.Now()
	text := fmt.Sprintf("Não consegui reconectar a Proton sozinho (%v).\n\n"+
		"Na janela da Proton: clique em Desconectar, depois vá em Países, escolha um país fora do Brasil (por exemplo Estados Unidos) e conecte.\n\n"+
		"Não use o botão Conectar grande: ele vai para o \"País mais rápido\", que costuma ser o Brasil.\n\n"+
		"Quando terminar, clique OK aqui.", cause)
	if messageBox(text, mbOKCancel|mbIconWarning) != idOK {
		return errors.New("cancelado. O Discord nao foi aberto para nao sair fora da VPN")
	}
	server, err := waitConnection(a.local, since, 2*time.Minute)
	if err != nil {
		return errors.New("a Proton nao reconectou. O Discord nao foi aberto para nao sair fora da VPN")
	}
	a.log.Printf("reconectado a mao em %s", server)
	return checkServer(server)
}

func waitConnection(local string, since time.Time, timeout time.Duration) (string, error) {
	var server string
	err := waitUntil(timeout, func() bool {
		s, at, ok := lastConnection(local)
		if ok && at.After(since) && tunnelUp() {
			server = s
			return true
		}
		return false
	}, "a Proton nao confirmou a conexao")
	return server, err
}

func checkServer(server string) error {
	if serverCountry(server) == "BR" {
		return fmt.Errorf("a Proton conectou num servidor do Brasil (%s), e por ele o Go Live continua bloqueado. Conecte em outro pais e abra de novo", server)
	}
	return nil
}

// O Discord pode instalar uma versao nova na propria abertura e reiniciar ja da pasta
// nova. Sem essa vigia, o dia da atualizacao seria justamente o dia que nao funciona.
func (a *app) watch(target Install, duration time.Duration) error {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)

		paths, _ := processPaths(target.Channel.Binary)
		if len(paths) == 0 {
			continue
		}
		_, list, plan, err := a.plan(detectInstalls(a.local))
		if err != nil {
			return err
		}
		fresh := false
		for _, path := range paths {
			if !list.active(path) {
				fresh = true
			}
		}
		// Pasta nova que ainda nao passou na checagem de completa vira plano vazio:
		// espera a proxima volta.
		if !fresh || !plan.changesTunnel() {
			continue
		}

		a.log.Printf("o %s abriu de uma pasta nova (%s), atualizando a Proton", target.Channel.Friendly, paths[0])
		err = a.updateInTurn()
		if errors.Is(err, errReconnectDeclined) {
			return nil
		}
		if err != nil {
			return err
		}
		return a.checkRoute(target)
	}
	return nil
}

// Pega a vez e refaz o plano ja com ela: outra copia que estava na frente pode ter
// resolvido a mesma atualizacao enquanto esta esperava.
func (a *app) updateInTurn() error {
	release, err := a.takeTurn()
	if err != nil {
		return err
	}
	defer release()

	installs := detectInstalls(a.local)
	settings, list, plan, err := a.plan(installs)
	if err != nil || !plan.changesTunnel() {
		return err
	}
	return a.update(settings, list, plan, installs)
}

func trimLog(path string, max, keep int64) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= max {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	os.WriteFile(path, data[int64(len(data))-keep:], 0o600)
}
