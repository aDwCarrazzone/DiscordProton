# DiscordProton

Abre o Discord garantindo que ele passe pela **Proton VPN**, e só ele. O resto do computador continua na internet normal.

Foi feito para quem usa o *split tunneling* da Proton no modo **Incluir** para recuperar o Go Live e a câmera no Brasil sem colocar o computador inteiro na VPN.

## O problema que ele resolve

A Proton reconhece os apps da lista do split tunneling pelo **caminho exato** do executável. O Discord roda de dentro de uma pasta com a versão no nome:

```
%LOCALAPPDATA%\Discord\app-1.0.9260\Discord.exe
```

Com essa pasta, há dois problemas:

- **A pasta muda a cada atualização "host" do Discord**, em geral uma vez por semana. A partir daí, a Proton não reconhece mais o Discord, ele volta a sair pela internet de casa e o botão de transmitir aparece bloqueado.
- **O `Discord.exe` da raiz** (`%LOCALAPPDATA%\Discord\Discord.exe`) só dispara o `Update.exe` e sai. Colocar esse na lista não adianta.

Além disso, a Proton só aplica a lista na hora de conectar, e o app dela não tem comando para reconectar.

## O que ele faz

A cada clique no atalho:

1. Descobre a versão atual de cada Discord instalado (normal, PTB e Canary). Ignora pastas de atualização interrompida.
2. Lê a lista de split tunneling da Proton. **Se já está em dia, só abre o Discord.**
3. **Se o Discord mudou de pasta:**
   1. **Pergunta antes de reconectar.**
   2. Fecha o app da Proton (o túnel continua de pé), faz backup da configuração e grava a lista nova.
   3. Reabre a Proton e aperta **Desconectar** e **Conectar** na mesma conexão de antes, pela aba Recentes.
   4. Confirma no log da Proton que ela conectou fora do Brasil.
4. Abre o Discord e o reinicia se ele estava rodando fora do túnel.
5. Vigia o Discord por 3 minutos. Se ele se atualizar na própria abertura e voltar de uma pasta nova, repete o passo 3.

Se a Proton estiver desconectada, ele conecta na conexão recente mais nova que não seja do Brasil antes de abrir o Discord. Ele nunca escolhe o "País mais rápido", que costuma cair no Brasil, nem quando essa era a conexão anterior.

Cliques seguidos em atalhos diferentes entram numa fila: só um por vez mexe na Proton, e o seguinte espera a vez e abre o seu Discord. A vigia depois de abrir não segura a fila.

### O que ele mexe

- **Configuração da Proton:** só a chave `SplitTunnelingInverseAppsList` do `UserSettings.*.json`.
  - Os outros valores, inclusive os cifrados, voltam idênticos byte a byte.
  - Itens que não são do Discord não são tocados.
  - Das entradas do Discord, só saem as de versões que já não existem no disco.
- **Backup:** a configuração anterior vai para `%LOCALAPPDATA%\DiscordProton\backup` (ficam as 10 mais recentes).
- **Registro:** `%LOCALAPPDATA%\DiscordProton\discordproton.log`.

Ele não precisa de administrador, não instala nada e não fala com nenhum servidor.

### Por que ele pergunta antes de reconectar

Nos segundos em que a Proton está "Conectando", uma conexão nova de qualquer programa pode nascer presa no túnel, e a que já estava nele cai. Isso foi visto com um jogo aberto: a conexão dele com o servidor acabou indo pela VPN. Se houver jogo aberto, feche antes ou escolha **Cancelar**. O Discord abre sem VPN e a pergunta volta no próximo clique.

A própria Proton tem esse comportamento ao conectar, com ou sem este programa. Conecte a VPN antes de abrir jogos.

## Requisitos

- Windows 10 ou 11.
- [Proton VPN](https://protonvpn.com/download) com plano pago. O split tunneling não existe no grátis.
- Na Proton: **Configurações → Split tunneling → ligado, modo Incluir**.
- [Go](https://go.dev/dl/) 1.22 ou mais novo, para compilar.

## Instalação

```
git clone https://github.com/aDwCarrazzone/DiscordProton.git
cd DiscordProton
go build -ldflags "-H=windowsgui -s -w" -o DiscordProton.exe .
```

Depois clique com o botão direito em `Criar-atalhos.ps1` e escolha **Executar com o PowerShell**. Ele cria na área de trabalho um atalho "Discord (Proton)" para cada Discord instalado. Use esses atalhos no lugar dos normais.

### Opções

| opção | para que serve |
| --- | --- |
| `-channel stable` | qual Discord abrir: `stable`, `ptb` ou `canary` |
| `-check` | só mostra o que mudaria na lista da Proton, sem mexer em nada |
| `-watch 3m` | quanto tempo vigiar o Discord depois de abrir |

## Como funciona por dentro

```
main.go             fluxo: conferir a lista, atualizar, reconectar, abrir e vigiar
discord.go          acha a versão atual de cada canal e abre o Discord pelo Update.exe
proton.go           lê e grava a configuração da Proton, confere túnel e log de conexão
uia.go / uia.ps1    aperta os botões da Proton pela automação de interface do Windows
winapi_windows.go   caminho dos processos, instância única e caixas de mensagem
Criar-atalhos.ps1   atalhos na área de trabalho
```

Os botões são encontrados pelo `AutomationId` (`ConnectionCardDisconnectButton`, `Connect_to_<país>`, `ConnectionsPageItem`). São os mesmos que a Proton usa nos [testes de interface dela](https://github.com/ProtonVPN/win-app/tree/master/src/Tests/ProtonVPN.UI.Tests), o que os torna estáveis entre versões. Se uma versão nova da Proton mudar a tela e a automação falhar, o programa pede para você reconectar à mão e espera, em vez de abrir o Discord fora da VPN.

O script de automação é passado ao PowerShell por `-EncodedCommand`, sem arquivo temporário.

## Testes

```
go test ./...
```

Para conferir contra a configuração real da sua Proton, só lendo, sem gravar nada:

```
set DISCORDPROTON_REAL_SETTINGS=1
go test -run Real -v ./...
```

## Limitações conhecidas

- Depende do formato do `UserSettings.*.json` e dos `AutomationId` da Proton para Windows (testado na 5.1.8).
- A Proton não tem comando para reconectar. Se a automação de interface quebrar, sobra o aviso para reconectar à mão.
- Só o Discord oficial (normal, PTB e Canary) instalado em `%LOCALAPPDATA%`.

## Licença

[GPL-3.0](LICENSE). Quem distribuir uma versão modificada precisa publicar o código dela sob a mesma licença.
