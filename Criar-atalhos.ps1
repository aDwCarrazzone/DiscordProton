# Cria na area de trabalho um atalho para cada Discord instalado (normal, PTB e Canary),
# todos abrindo pelo DiscordProton.exe.
# Uso: clique com o botao direito neste arquivo e escolha "Executar com o PowerShell".

$ErrorActionPreference = 'Stop'

$pasta = Split-Path -Parent $MyInvocation.MyCommand.Path
$alvo = Join-Path $pasta 'DiscordProton.exe'

if (-not (Test-Path -LiteralPath $alvo)) {
    Write-Host ''
    Write-Host 'Nao encontrei o DiscordProton.exe nesta pasta.' -ForegroundColor Red
    Write-Host 'Compile com: go build -ldflags "-H=windowsgui -s -w" -o DiscordProton.exe .'
    Write-Host ''
    Read-Host 'Pressione Enter para fechar'
    exit 1
}

$canais = @(
    @{ Pasta = 'Discord';       Chave = 'stable'; Nome = 'Discord' },
    @{ Pasta = 'DiscordPTB';    Chave = 'ptb';    Nome = 'Discord PTB' },
    @{ Pasta = 'DiscordCanary'; Chave = 'canary'; Nome = 'Discord Canary' }
)

$areaDeTrabalho = [Environment]::GetFolderPath('Desktop')
$shell = New-Object -ComObject WScript.Shell
$criados = @()

foreach ($canal in $canais) {
    $raiz = Join-Path $env:LOCALAPPDATA $canal.Pasta
    if (-not (Test-Path (Join-Path $raiz 'app-*') -PathType Container)) { continue }

    $titulo = "$($canal.Nome) (Proton)"
    $atalho = $shell.CreateShortcut((Join-Path $areaDeTrabalho "$titulo.lnk"))
    $atalho.TargetPath = $alvo
    $atalho.Arguments = "-channel $($canal.Chave)"
    $atalho.WorkingDirectory = $pasta
    $atalho.Description = "Abre o $($canal.Nome) garantindo que ele passe pela Proton VPN"
    # O app.ico da raiz sobrevive as atualizacoes; o exe dentro de app-<versao> nao.
    $icone = Join-Path $raiz 'app.ico'
    if (Test-Path -LiteralPath $icone) { $atalho.IconLocation = $icone }
    $atalho.Save()

    $criados += $titulo
}

Write-Host ''
if ($criados.Count -eq 0) {
    Write-Host 'Nao achei nenhum Discord instalado dentro de %LOCALAPPDATA%.' -ForegroundColor Red
    Write-Host ''
    Read-Host 'Pressione Enter para fechar'
    exit 1
}

Write-Host 'Pronto. Atalhos criados na sua area de trabalho:' -ForegroundColor Green
foreach ($titulo in $criados) { Write-Host "  - $titulo" }
Write-Host 'Use eles no lugar dos atalhos normais do Discord.'
Write-Host ''
Read-Host 'Pressione Enter para fechar'
