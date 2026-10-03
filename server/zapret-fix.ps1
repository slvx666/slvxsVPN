# Настройка zapret (Flowseal zapret-discord-youtube) для работы вместе с VPN (Clash Verge).
# Сервер подставляет свои адреса при выдаче скрипта.
#  1) IP VPN-сервера -> lists\ipset-exclude-user.txt (штатный список исключений Flowseal)
#     и --ipset-exclude-ip в каждый профиль (у профилей Google/Discord общего исключения нет):
#     zapret не трогает подключение к VPN.
#  2) --wf-iface: zapret работает только на реальном адаптере (Wi-Fi/кабель). Иначе он подмешивает пакеты в туннель
#     Clash (адреса 198.18.x.x zapret не считает локальными) и ломает YouTube, Discord, Telegram и т.д.
#  3) Если zapret запущен батником (general*.bat) — переводит его в службу zapret, как это делает service.bat Flowseal:
#     запускается вместе с Windows уже с правильными настройками.
# Показывает старую и новую команду, спрашивает подтверждение; если zapret не запустится — возвращает как было.
$ErrorActionPreference = 'Stop'
$Server = '__SERVER__'; $UpUrl = '__UP__'
$Log = New-Object System.Text.StringBuilder
function Say([string]$s, [string]$c = 'Gray') { [void]$Log.AppendLine($s); Write-Host $s -ForegroundColor $c }
function Send {
    try {
        $w = New-Object Net.WebClient; $w.Headers['Content-Type'] = 'text/plain; charset=utf-8'
        [void]$w.UploadData($UpUrl, 'POST', [Text.Encoding]::UTF8.GetBytes('zapret-fix ' + (Get-Date -Format 's') + "`n" + $Log.ToString()))
    } catch {}
}
function Running { [bool](Get-Process winws -ErrorAction SilentlyContinue) }

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) {
    Write-Host 'Нужны права администратора: Win+X -> «Терминал (администратор)», затем вставьте команду ещё раз.' -ForegroundColor Yellow
    return
}

# --- где и как запущен zapret
$key = 'HKLM:\SYSTEM\CurrentControlSet\Services\zapret'
$svcCmd = $null; try { $svcCmd = (Get-ItemProperty $key -ErrorAction Stop).ImagePath } catch {}
$proc = Get-CimInstance Win32_Process -Filter "Name='winws.exe'" | Select-Object -First 1
if ($svcCmd -and $svcCmd -match 'winws') { $mode = 'service'; $old = $svcCmd }
elseif ($proc -and $proc.CommandLine) { $mode = 'bat'; $old = $proc.CommandLine.Trim() }
else { Say 'zapret сейчас не запущен. Запустите ваш general*.bat (тот, с которым YouTube работает) и повторите команду.' 'Yellow'; Send; return }
Say ('zapret запущен ' + $(if ($mode -eq 'service') { 'как служба' } else { 'батником (winws.exe, PID ' + $proc.ProcessId + ')' }))
Say ('Текущая команда: ' + $old)
if ($old -notmatch '^\s*("[^"]*winws\.exe"|\S*winws\.exe)(.*)$') { Say 'Не удалось разобрать команду zapret — ничего не меняю.' 'Red'; Send; return }
$exe = $Matches[1]; $rest = $Matches[2]
$exePath = $exe.Trim('"')
$root = Split-Path (Split-Path $exePath -Parent) -Parent
Say ('Папка zapret: ' + $root)

# --- реальный адаптер = маршрут по умолчанию с наименьшей метрикой среди физических (не туннели и не виртуальные)
$tun = 'Meta|Wintun|TAP-|WireGuard|Amnezia|Tunnel|Happ|sing-|Clash|VPN|Hyper-V|Loopback'
$phys = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | ForEach-Object {
    $a = Get-NetAdapter -InterfaceIndex $_.ifIndex -ErrorAction SilentlyContinue
    if ($a -and $a.Status -eq 'Up' -and ($a.InterfaceDescription + ' ' + $a.Name) -notmatch $tun) {
        $im = (Get-NetIPInterface -InterfaceIndex $_.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue).InterfaceMetric
        [pscustomobject]@{ Idx = $_.ifIndex; Name = $a.Name; Desc = $a.InterfaceDescription; Metric = $_.RouteMetric + $im }
    }
} | Sort-Object Metric | Select-Object -First 1
if (-not $phys) { Say 'Не нашёл реальный сетевой адаптер с выходом в интернет — ничего не меняю.' 'Red'; Send; return }
Say ('Реальный адаптер: ' + $phys.Name + ' [' + $phys.Desc + '], индекс ' + $phys.Idx)

# --- новая команда: убираем прежние наши параметры, --wf-iface в начало, исключение сервера — в каждый профиль (--new)
$rest = [regex]::Replace($rest, '\s+--wf-iface=\S+', '')
$rest = [regex]::Replace($rest, '\s+--ipset-exclude-ip=' + [regex]::Escape($Server) + '(?=\s|$)', '')
$parts = [regex]::Split($rest.Trim(), '\s+--new(?=\s|$)') | ForEach-Object { $_.Trim() + ' --ipset-exclude-ip=' + $Server }
$new = $exe + ' --wf-iface=' + $phys.Idx + ' ' + ($parts -join ' --new ')
Say ''
Say ('Новая команда: ' + $new) 'Cyan'
Say ''
if ($mode -eq 'bat') { Say 'zapret будет переведён в службу (как делает service.bat): стартует вместе с Windows, батник больше запускать не нужно.' 'Cyan' }
$answer = (Read-Host 'Применить? Введите y (или н / да) и Enter, любое другое — отмена').Trim().ToLower()
if ($answer -notin 'y', 'yes', 'н', 'д', 'да') { Say ('Отменено (ответ: «' + $answer + '»), ничего не изменено.'); Send; return }

# --- 1) штатный список исключений Flowseal
$ex = Join-Path $root 'lists\ipset-exclude-user.txt'
try {
    $lines = @(); if (Test-Path $ex) { $lines = @(Get-Content $ex) }
    if ($lines -notcontains $Server) { Add-Content -Path $ex -Value $Server -Encoding ASCII; Say ('Добавил ' + $Server + ' в ' + $ex) } else { Say ('В ' + $ex + ' сервер уже есть') }
} catch { Say ('Не удалось изменить ' + $ex + ': ' + $_.Exception.Message) 'Yellow' }

# --- 2) метки времени TCP — нужны стратегиям Flowseal (service.bat включает их так же)
try { netsh interface tcp set global timestamps=enabled | Out-Null } catch {}

$bak = Join-Path $env:ProgramData ('zapret-cmd-backup-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.txt')
[IO.File]::WriteAllText($bak, $mode + "`n" + $old, [Text.Encoding]::UTF8)
function WaitRunning { Start-Sleep -Seconds 4; ((Get-Service zapret -ErrorAction SilentlyContinue).Status -eq 'Running') -and (Running) }

if ($mode -eq 'service') {
    Set-ItemProperty -Path $key -Name ImagePath -Value $new -Type ExpandString
    try { Restart-Service zapret -Force -ErrorAction Stop } catch {}
    $ok = WaitRunning
    if (-not $ok) {
        Say 'zapret не запустился с новыми параметрами — возвращаю как было.' 'Red'
        Set-ItemProperty -Path $key -Name ImagePath -Value $old -Type ExpandString
        try { Restart-Service zapret -Force -ErrorAction Stop } catch {}
    }
} else {
    $title = (Get-Process -Id $proc.ProcessId -ErrorAction SilentlyContinue).MainWindowTitle
    $strategy = if ($title -match '^zapret:\s*(.+)$') { $Matches[1].Trim() } else { 'general (VPN)' }
    Stop-Process -Name winws -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    try { sc.exe stop zapret | Out-Null; sc.exe delete zapret | Out-Null } catch {}
    $ok = $false
    try {
        New-Service -Name zapret -BinaryPathName $new -DisplayName 'zapret' -StartupType Automatic -Description 'Zapret DPI bypass software' | Out-Null
        New-ItemProperty -Path $key -Name 'zapret-discord-youtube' -Value $strategy -PropertyType String -Force | Out-Null
        try { Start-Service zapret -ErrorAction Stop } catch {}
        $ok = WaitRunning
    } catch { Say ('Не удалось создать службу: ' + $_.Exception.Message) 'Red' }
    if (-not $ok) {
        Say 'zapret не запустился как служба — убираю службу и запускаю zapret как было.' 'Red'
        try { sc.exe stop zapret | Out-Null } catch {}; sc.exe delete zapret | Out-Null
        Start-Process -FilePath $exePath -ArgumentList $rest.Trim() -WorkingDirectory (Split-Path $exePath -Parent) -WindowStyle Minimized
        Start-Sleep -Seconds 2
    }
}
if ($ok) {
    Say ('Готово: zapret работает службой только на адаптере «' + $phys.Name + '» и не трогает VPN-сервер ' + $Server + '.') 'Green'
    Say 'Если смените подключение (Wi-Fi <-> кабель) или переустановите zapret — запустите эту команду ещё раз.' 'Green'
} elseif (Running) { Say 'Вернул прежний режим — zapret работает как раньше.' 'Yellow' }
else { Say 'ВНИМАНИЕ: zapret сейчас не работает — запустите ваш general*.bat вручную.' 'Red' }
Say ('Копия старой команды: ' + $bak)
Send
