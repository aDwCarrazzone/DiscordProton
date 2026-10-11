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
	if !plan.empty() {
		err := a.update(settings, list, plan, installs)
		switch {
		case errors.Is(err, errReconnectDeclined):
			declined = true
		case err != nil:
			return err
		}
	} else if err := a.ensureConnected(installs); err != nil {
		return err
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
// reconectar. Por isso: fecha o app, grava a lista, reabre e aperta Desconectar e
// Conectar na janela dela.
func (a *app) update(settings *protonSettings, list appList, plan syncPlan, installs []Install) error {
	a.log.Print(describePlan(list, plan))

	text, err := list.apply(plan)
	if err != nil {
		return err
	}
	if err := settings.setStr(keyAppsList, text); err != nil {
		return err
	}

	wasUp := tunnelUp()
	if wasUp {
		if !confirmReconnect() {
			a.log.Print("reconexao recusada, a lista da Proton fica como estava")
			return errReconnectDeclined
		}
		a.log.Print("reconexao confirmada")
	}

	outside := a.discordsOutside(installs, list)
	if !wasUp {
		outside = a.discordsOutside(installs, appList{})
	}

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

	if err := showProtonClient(); err != nil {
		return fmt.Errorf("nao consegui reabrir a Proton: %w", err)
	}

	action := "connect"
	if wasUp {
		action = "reconnect"
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
	text := "O Discord foi atualizado e a Proton precisa reconectar para incluir a versão nova (uns 5 segundos).\n\n" +
		"Se tiver um jogo aberto, feche antes ou espere a partida acabar: o que se conectar durante a reconexão pode acabar indo pela VPN.\n\n" +
		"OK: reconectar agora.\nCancelar: abrir o Discord assim mesmo, sem VPN (o Go Live fica bloqueado até a próxima vez)."
	return messageBox(text, mbOKCancel|mbIconWarning) == idOK
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
		if !fresh || plan.empty() {
			continue
		}

		a.log.Printf("o %s abriu de uma pasta nova (%s), atualizando a Proton", target.Channel.Friendly, paths[0])
		err = a.updateInTurn()
		if errors.Is(err, errReconnectDeclined) {
			return nil
		}
		return err
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
	if err != nil || plan.empty() {
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
