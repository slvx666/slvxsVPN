# Поиск причины периодических (раз в 4 с) подвисаний Wi-Fi (Windows, от администратора). Сервер подставляет адрес отчёта.
# Запускать, когда открыта игра (лучше — стрельбище в Apex), окно игры можно свернуть.
# По 40 с пингует роутер каждые 100 мс в трёх режимах и сравнивает скачки:
#   A — сканирование Wi-Fi Windows включено (игровой режим на паузе);
#   B — сканирование выключено (как делает игровой режим);
#   C — сканирование включено, Bluetooth выключен (если есть).
# Плюс: журнал игрового режима, программы, работающие с Wi-Fi (wlanapi.dll), устройства Bluetooth.
# После теста всё возвращается как было. Отчёт уходит на сервер.
$ErrorActionPreference = 'Continue'
$UpUrl = '__UP__'; $Task = 'VPN Game Mode'
$Log = New-Object System.Text.StringBuilder
function Say([string]$s, [string]$c = 'Gray') { [void]$Log.AppendLine($s); Write-Host $s -ForegroundColor $c }
function Send {
    try {
        $w = New-Object Net.WebClient; $w.Headers['Content-Type'] = 'text/plain; charset=utf-8'
        [void]$w.UploadData($UpUrl, 'POST', [Text.Encoding]::UTF8.GetBytes('wifi-probe ' + (Get-Date -Format 's') + "`n" + $Log.ToString()))
    } catch {}
}
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { Write-Host 'Нужны права администратора: Win+X -> «Терминал (администратор)», затем вставьте команду ещё раз.' -ForegroundColor Yellow; return }

$a = Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.Status -eq 'Up' -and $_.NdisPhysicalMedium -eq 9 } | Select-Object -First 1
if (-not $a) { Say 'Подключённый Wi-Fi-адаптер не найден.' 'Yellow'; Send; return }
$gw = (Get-NetRoute -DestinationPrefix '0.0.0.0/0' -InterfaceIndex $a.ifIndex -ErrorAction SilentlyContinue | Sort-Object RouteMetric | Select-Object -First 1).NextHop
Say ('Адаптер: ' + $a.Name + ' [' + $a.InterfaceDescription + '], роутер ' + $gw)
$game = Get-Process | Where-Object { $_.ProcessName -match '^(r5apex|r5apex_dx12|cs2|dota2|VALORANT-Win64-Shipping|FortniteClient-Win64-Shipping)$' } | Select-Object -First 1
Say ('Игра запущена: ' + $(if ($game) { $game.ProcessName } else { 'нет (лучше запустить игру и повторить — подвисания появлялись во время игры)' }))

Say ''; Say '--- журнал игрового режима (последние записи)'
Get-Content (Join-Path $env:ProgramData 'VPN-GameMode\log.txt') -Tail 12 -ErrorAction SilentlyContinue | ForEach-Object { Say ('  ' + $_) }
Say '--- программы, работающие с Wi-Fi (загружен wlanapi.dll)'
Get-Process | ForEach-Object { try { if ($_.Modules.ModuleName -contains 'wlanapi.dll') { Say ('  ' + $_.ProcessName + '  ' + $_.Path) } } catch {} }
Say '--- Bluetooth'
Get-PnpDevice -Class Bluetooth -PresentOnly -ErrorAction SilentlyContinue | Where-Object { $_.FriendlyName -notmatch 'Enumerator|RFCOMM|LE Generic|Microsoft Bluetooth LE' } | ForEach-Object { Say ('  ' + $_.Status + '  ' + $_.FriendlyName) }

# --- пинг роутера N секунд каждые 100 мс: скачки, потери и «таймерность» (доля скачков в одной фазе 4-секундного цикла)
$pinger = New-Object System.Net.NetworkInformation.Ping
function Probe([string]$name, [int]$sec = 40) {
    Write-Host ('Тест ' + $name + ' (' + $sec + ' с)...') -ForegroundColor Cyan
    $res = @(); $sw = [Diagnostics.Stopwatch]::StartNew()
    while ($sw.Elapsed.TotalSeconds -lt $sec) {
        $t = $sw.Elapsed.TotalSeconds; $r = -1
        try { $p = $pinger.Send($gw, 1000); if ($p.Status -eq 'Success') { $r = [int]$p.RoundtripTime } } catch {}
        $res += , @($t, $r)
        $left = 100 - [int](($sw.Elapsed.TotalSeconds - $t) * 1000); if ($left -gt 0) { Start-Sleep -Milliseconds $left }
    }
    $spk = @($res | Where-Object { $_[1] -lt 0 -or $_[1] -gt 50 })
    $ph = $spk | Group-Object { [math]::Floor(($_[0] % 4) * 2.5) } | Sort-Object Count -Descending | Select-Object -First 1
    $timer = if ($spk.Count -ge 4 -and $ph) { [math]::Round($ph.Count * 100 / $spk.Count) } else { 0 }
    $ok = @($res | Where-Object { $_[1] -ge 0 } | ForEach-Object { $_[1] } | Sort-Object)
    Say ('  {0}: замеров {1}, скачков >50 мс: {2}, потерь: {3}, макс {4} мс, из скачков «по таймеру 4 с»: {5}%' -f $name, $res.Count, $spk.Count, @($res | Where-Object { $_[1] -lt 0 }).Count, $(if ($ok.Count) { $ok[-1] } else { '-' }), $timer)
    [pscustomobject]@{ Name = $name; Spikes = $spk.Count; Timer = $timer }
}
function Scan([bool]$on) { netsh wlan set autoconfig enabled=$(if ($on) { 'yes' } else { 'no' }) interface="$($a.Name)" | Out-Null }

# --- Bluetooth через WinRT (Windows PowerShell 5.1)
$bt = $null
try {
    Add-Type -AssemblyName System.Runtime.WindowsRuntime
    $asTask = ([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation`1' })[0]
    function Await($op, $type) { $t = $asTask.MakeGenericMethod($type).Invoke($null, @($op)); $t.Wait(-1) | Out-Null; $t.Result }
    [Windows.Devices.Radios.Radio,Windows.System.Devices,ContentType=WindowsRuntime] | Out-Null
    [Windows.Devices.Radios.RadioAccessStatus,Windows.System.Devices,ContentType=WindowsRuntime] | Out-Null
    [Windows.Devices.Radios.RadioState,Windows.System.Devices,ContentType=WindowsRuntime] | Out-Null
    [void](Await ([Windows.Devices.Radios.Radio]::RequestAccessAsync()) ([Windows.Devices.Radios.RadioAccessStatus]))
    $bt = (Await ([Windows.Devices.Radios.Radio]::GetRadiosAsync()) ([System.Collections.Generic.IReadOnlyList[Windows.Devices.Radios.Radio]])) | Where-Object { $_.Kind -eq 'Bluetooth' } | Select-Object -First 1
} catch { $bt = $null }
$btWasOn = $bt -and "$($bt.State)" -eq 'On'
Say ('Bluetooth-радио: ' + $(if ($bt) { "$($bt.State)" } else { 'нет или недоступно' }))

Say ''; Say 'Тесты (игровой режим на паузе, потом всё вернётся):' 'Cyan'
$hadTask = [bool](Get-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue)
if ($hadTask) { Stop-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue }
$r = @()
try {
    Scan $true; Start-Sleep -Seconds 2; $r += Probe 'A: сканирование ВКЛ'
    Scan $false; Start-Sleep -Seconds 2; $r += Probe 'B: сканирование ВЫКЛ (игровой режим)'
    Scan $true
    if ($btWasOn) {
        [void](Await ($bt.SetStateAsync([Windows.Devices.Radios.RadioState]::Off)) ([Windows.Devices.Radios.RadioAccessStatus])); Start-Sleep -Seconds 2
        $r += Probe 'C: сканирование ВКЛ, Bluetooth ВЫКЛ'
    }
} finally {
    Scan $true
    if ($btWasOn) { try { [void](Await ($bt.SetStateAsync([Windows.Devices.Radios.RadioState]::On)) ([Windows.Devices.Radios.RadioAccessStatus])) } catch {} }
    if ($hadTask) { Start-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue }
}
Say ''; Say 'Всё возвращено как было (сканирование, Bluetooth, игровой режим).' 'Green'
$best = $r | Sort-Object Spikes | Select-Object -First 1
Say ('Меньше всего скачков: ' + $best.Name) 'Green'
Send
Write-Host 'Отчёт отправлен.' -ForegroundColor Green
