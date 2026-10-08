# Aperta os botoes do app da Proton pela automacao de interface do Windows. Os
# AutomationId usados aqui sao os mesmos que a propria Proton usa nos testes de
# interface dela (src/Tests/ProtonVPN.UI.Tests), o que os torna estaveis entre versoes.
#
# O Go prefixa $Action ('state', 'connect' ou 'reconnect') e $TimeoutSec. A saida e uma
# linha JSON so: {"ok":true,...} ou {"ok":false,"error":"..."}.

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes

$AE = [System.Windows.Automation.AutomationElement]
$Scope = [System.Windows.Automation.TreeScope]
$noBrasil = '^(Brasil|Brazil)$'
# A linha "Pais mais rapido" pode cair no Brasil, que e justamente onde nao se pode sair.
$naoEscolher = '(Brasil|Brazil|r[aá]pido|Fastest)'

function Finish($result) {
    $result | ConvertTo-Json -Compress
    exit 0
}

function Fail($message) {
    Finish @{ ok = $false; error = $message }
}

function Wait-For([scriptblock]$probe, [int]$seconds) {
    $end = (Get-Date).AddSeconds($seconds)
    do {
        $value = & $probe
        if ($value) { return $value }
        Start-Sleep -Milliseconds 400
    } while ((Get-Date) -lt $end)
    return $null
}

function Find-Window {
    foreach ($id in @(Get-Process ProtonVPN.Client -ErrorAction SilentlyContinue | ForEach-Object Id)) {
        $cond = New-Object System.Windows.Automation.PropertyCondition($AE::ProcessIdProperty, $id)
        $window = $AE::RootElement.FindFirst($Scope::Children, $cond)
        if ($window) { return $window }
    }
    return $null
}

function Find-Id($root, [string]$id) {
    $cond = New-Object System.Windows.Automation.PropertyCondition($AE::AutomationIdProperty, $id)
    return $root.FindFirst($Scope::Descendants, $cond)
}

function Press($element) {
    $element.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke()
}

function Card($window) {
    $title = Find-Id $window 'ConnectionCardTitle'
    $detail = Find-Id $window 'ConnectionCardDescription'
    return [pscustomobject]@{
        title     = $(if ($title) { $title.Current.Name } else { '' })
        detail    = $(if ($detail) { $detail.Current.Name } else { '' })
        connected = [bool](Find-Id $window 'ConnectionCardDisconnectButton')
    }
}

# A lista ao lado do cartao pode estar em Paises ou Perfis; em Paises, o primeiro
# botao seria o primeiro pais em ordem alfabetica. Recentes e sempre a primeira aba.
function Show-Recents($window) {
    $cond = New-Object System.Windows.Automation.PropertyCondition($AE::AutomationIdProperty, 'ConnectionsPageItem')
    $tabs = $window.FindAll($Scope::Descendants, $cond)
    if ($tabs.Count -eq 0) { return $false }
    $pattern = $null
    if ($tabs[0].TryGetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern, [ref]$pattern)) {
        $pattern.Select()
        Start-Sleep -Milliseconds 600
        return $true
    }
    return $false
}

function Recent-Buttons($window) {
    $cond = New-Object System.Windows.Automation.PropertyCondition($AE::ControlTypeProperty, [System.Windows.Automation.ControlType]::Button)
    $buttons = $window.FindAll($Scope::Descendants, $cond)
    $found = @()
    foreach ($button in $buttons) {
        $id = $button.Current.AutomationId
        if ($id -like 'Connect_to_*' -and -not $button.Current.IsOffscreen) { $found += $button }
    }
    return $found
}

function Connect-To($window, [string]$preferred) {
    if (-not (Show-Recents $window)) { Fail 'nao achei a aba Recentes da Proton' }

    $target = $null
    if ($preferred -and $preferred -notmatch $noBrasil) {
        $target = Wait-For { Find-Id $window "Connect_to_$preferred" } 10
    }
    if (-not $target) {
        foreach ($button in (Recent-Buttons $window)) {
            $name = $button.Current.AutomationId.Substring('Connect_to_'.Length)
            if ($name -notmatch $naoEscolher) { $target = $button; break }
        }
    }
    if (-not $target) { Fail 'nenhuma conexao recente fora do Brasil para usar' }

    Press $target
    $card = Wait-For { $c = Card $window; if ($c.connected) { $c } } $TimeoutSec
    if (-not $card) { Fail 'a Proton nao terminou de conectar a tempo' }
    if ($card.title -match $noBrasil) { Fail "a Proton conectou no Brasil ($($card.detail))" }
    Finish @{ ok = $true; title = $card.title; detail = $card.detail }
}

$window = Wait-For { Find-Window } $TimeoutSec
if (-not $window) { Fail 'a janela da Proton nao apareceu' }
if (-not (Wait-For { Find-Id $window 'ConnectionCardComponent' } $TimeoutSec)) { Fail 'a tela inicial da Proton nao carregou' }

switch ($Action) {
    'state' {
        $card = Card $window
        Finish @{ ok = $true; title = $card.title; detail = $card.detail; connected = $card.connected }
    }
    'connect' {
        $card = Card $window
        if ($card.connected) { Finish @{ ok = $true; title = $card.title; detail = $card.detail } }
        Connect-To $window ''
    }
    'reconnect' {
        $before = Card $window
        $button = Find-Id $window 'ConnectionCardDisconnectButton'
        if ($button) {
            Press $button
            if (-not (Wait-For { -not (Find-Id $window 'ConnectionCardDisconnectButton') } 30)) { Fail 'a Proton nao desconectou' }
        }
        Connect-To $window $before.title
    }
    default { Fail "acao desconhecida: $Action" }
}
