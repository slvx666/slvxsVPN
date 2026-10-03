# Монитор лагов на время игры (Windows). Сервер подставляет свои адреса при выдаче скрипта.
# Каждые ~300 мс: пинг до роутера (Wi-Fi) и запрос к VPN-серверу по уже открытому соединению (путь через провайдера).
# Раз в 10 с — сигнал Wi-Fi. Остановка — Enter. Итог: сохраняется на рабочий стол и уходит на сервер.
$ErrorActionPreference = 'Continue'
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls13 } catch { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 }
$SubUrl = '__SUB__'; $UpUrl = '__UP__'
Add-Type -AssemblyName System.Net.Http

# роутер = шлюз реального адаптера (не туннеля)
$tun = 'Meta|Wintun|TAP-|WireGuard|Amnezia|Tunnel|Happ|sing-|Clash|VPN|Hyper-V|Loopback'
$gw = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Where-Object {
    $a = Get-NetAdapter -InterfaceIndex $_.ifIndex -ErrorAction SilentlyContinue
    $a -and $a.Status -eq 'Up' -and ($a.InterfaceDescription + ' ' + $a.Name) -notmatch $tun -and $_.NextHop -ne '0.0.0.0'
} | Sort-Object RouteMetric | Select-Object -First 1
$gwIp = if ($gw) { $gw.NextHop } else { $null }

$h = New-Object System.Net.Http.HttpClientHandler; $h.UseProxy = $false
$client = New-Object System.Net.Http.HttpClient($h); $client.Timeout = [TimeSpan]::FromSeconds(4)
$pinger = New-Object System.Net.NetworkInformation.Ping
$rows = New-Object System.Collections.Generic.List[object]
$wifi = New-Object System.Collections.Generic.List[object]
function WifiNow {
    # строки netsh локализованы (и могут прийти в другой кодировке) — ищем по форме значения, а не по названию
    $o = @{}; $rates = @()
    foreach ($l in (netsh wlan show interfaces)) {
        if ($l -match ':\s*(\d{1,3})\s*%\s*$') { $o.Signal = [int]$Matches[1] }
        elseif ($l -match '\([^)]*\)\s*:\s*([\d.,]+)\s*$') { $rates += $Matches[1] }
        if ($l -match '(802\.11\w+)') { $o.Radio = $Matches[1] }
    }
    if ($rates.Count -ge 1) { $o.Rx = $rates[0] }; if ($rates.Count -ge 2) { $o.Tx = $rates[1] }
    $o
}

Write-Host ''
Write-Host 'Монитор лагов запущен. Идите играть — окно можно свернуть.' -ForegroundColor Cyan
Write-Host 'Когда закончите — вернитесь в это окно и нажмите Enter (максимум 60 минут).' -ForegroundColor Cyan
Write-Host ('Роутер: ' + $gwIp + '   Сервер: ' + ([uri]$SubUrl).Host) -ForegroundColor DarkGray
Write-Host ''
try { [void]$client.GetAsync($SubUrl + '/ping').Result } catch {}   # прогрев соединения
$start = Get-Date; $lastWifi = [datetime]::MinValue; $lastShow = [datetime]::MinValue
while (((Get-Date) - $start).TotalMinutes -lt 60) {
    try { if ([Console]::KeyAvailable) { $k = [Console]::ReadKey($true); if ($k.Key -eq 'Enter') { break } } } catch {}
    $now = Get-Date; $r = [ordered]@{ T = $now; Gw = -1; Srv = -1 }
    if ($gwIp) { try { $p = $pinger.Send($gwIp, 800); if ($p.Status -eq 'Success') { $r.Gw = [int]$p.RoundtripTime } } catch {} }
    $sw = [Diagnostics.Stopwatch]::StartNew()
    try { $resp = $client.GetAsync($SubUrl + '/ping').Result; if ($resp.IsSuccessStatusCode) { $r.Srv = [int]$sw.ElapsedMilliseconds }; $resp.Dispose() } catch {}
    $rows.Add([pscustomobject]$r)
    if (($now - $lastWifi).TotalSeconds -ge 10) { $w = WifiNow; $w.T = $now; $wifi.Add([pscustomobject]$w); $lastWifi = $now }
    if (($now - $lastShow).TotalSeconds -ge 1) {
        $bad = @($rows | Where-Object { $_.Srv -lt 0 -or $_.Srv -gt 150 -or $_.Gw -lt 0 -or $_.Gw -gt 50 }).Count
        Write-Host ("`r{0:HH:mm:ss}  роутер: {1,4} мс   сервер: {2,4} мс   сигнал Wi-Fi: {3,3}%   просадок: {4}      " -f $now, $(if ($r.Gw -ge 0) { $r.Gw } else { 'нет' }), $(if ($r.Srv -ge 0) { $r.Srv } else { 'нет' }), $wifi[-1].Signal, $bad) -NoNewline
        $lastShow = $now
    }
    $left = 300 - [int]((Get-Date) - $now).TotalMilliseconds; if ($left -gt 0) { Start-Sleep -Milliseconds $left }
}
Write-Host ''; Write-Host ''

# --- итог
$R = New-Object System.Text.StringBuilder
function L([string]$s) { [void]$R.AppendLine($s); Write-Host $s }
function P($a, $q) { if ($a.Count -eq 0) { return '-' }; $s = $a | Sort-Object; $s[[math]::Min($s.Count - 1, [int]($s.Count * $q))] }
function Leg([string]$name, [string]$prop, [int]$spike) {
    $ok = @($rows | Where-Object { $_.$prop -ge 0 } | ForEach-Object { $_.$prop }); $lost = @($rows | Where-Object { $_.$prop -lt 0 }).Count
    L ('  {0,-26} замеров {1}, потеряно {2} ({3:N1}%), мин/медиана/99%/макс = {4}/{5}/{6}/{7} мс, выше {8} мс: {9}' -f $name, $rows.Count, $lost, ($lost * 100.0 / [math]::Max(1, $rows.Count)), (P $ok 0), (P $ok 0.5), (P $ok 0.99), (P $ok 1.0), $spike, @($ok | Where-Object { $_ -gt $spike }).Count)
}
L ('Монитор лагов  ' + $start.ToString('yyyy-MM-dd HH:mm:ss') + ' — ' + (Get-Date).ToString('HH:mm:ss') + ' (' + [math]::Round(((Get-Date) - $start).TotalMinutes, 1) + ' мин)')
L ('Роутер ' + $gwIp + ', сервер ' + ([uri]$SubUrl).Host)
Leg 'ПК -> роутер (Wi-Fi)' 'Gw' 50
Leg 'ПК -> сервер (провайдер)' 'Srv' 150
$sig = @($wifi | Where-Object { $_.Signal } | ForEach-Object { $_.Signal })
if ($sig.Count) { L ('  Wi-Fi: сигнал мин/медиана/макс = {0}/{1}/{2}%, канал {3}, {4}, скорость приёма ~{5} Мбит/с' -f (P $sig 0), (P $sig 0.5), (P $sig 1.0), $wifi[-1].Ch, $wifi[-1].Radio, $wifi[-1].Rx) }
L ''
L 'Поминутно (роутер: медиана/макс/потери | сервер: медиана/макс/потери):'
$rows | Group-Object { $_.T.ToString('HH:mm') } | ForEach-Object {
    $g = @($_.Group | Where-Object { $_.Gw -ge 0 } | ForEach-Object { $_.Gw }); $s = @($_.Group | Where-Object { $_.Srv -ge 0 } | ForEach-Object { $_.Srv })
    L ('  {0}   {1,3}/{2,4}/{3,2}   |   {4,3}/{5,5}/{6,2}' -f $_.Name, (P $g 0.5), (P $g 1.0), @($_.Group | Where-Object { $_.Gw -lt 0 }).Count, (P $s 0.5), (P $s 1.0), @($_.Group | Where-Object { $_.Srv -lt 0 }).Count)
}
L ''
L 'Просадки (время: роутер / сервер):'
$ev = @($rows | Where-Object { $_.Srv -lt 0 -or $_.Srv -gt 150 -or $_.Gw -lt 0 -or $_.Gw -gt 50 })
foreach ($e in ($ev | Select-Object -First 300)) { L ('  {0:HH:mm:ss.f}   {1,5} / {2,5}' -f $e.T, $(if ($e.Gw -ge 0) { "$($e.Gw)мс" } else { 'нет' }), $(if ($e.Srv -ge 0) { "$($e.Srv)мс" } else { 'нет' })) }
if ($ev.Count -eq 0) { L '  нет' }
L ''; L 'Wi-Fi каждые 10 с (время сигнал% приём/передача Мбит/с):'
foreach ($w in $wifi) { L ('  {0:HH:mm:ss} {1,3}% {2}/{3}' -f $w.T, $w.Signal, $w.Rx, $w.Tx) }

$text = $R.ToString()
$desk = [Environment]::GetFolderPath('Desktop'); if (-not $desk -or -not (Test-Path $desk)) { $desk = $env:TEMP }
$file = Join-Path $desk ('vpn-lag-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.txt')
try { [IO.File]::WriteAllText($file, $text, [Text.Encoding]::UTF8) } catch {}
try {
    $w = New-Object Net.WebClient; $w.Headers['Content-Type'] = 'text/plain; charset=utf-8'
    [void]$w.UploadData($UpUrl, 'POST', [Text.Encoding]::UTF8.GetBytes($text))
    Write-Host ('Отчёт отправлен на сервер и сохранён: ' + $file) -ForegroundColor Green
} catch { Write-Host ('Отчёт сохранён: ' + $file + ' (отправить не удалось)') -ForegroundColor Yellow }
