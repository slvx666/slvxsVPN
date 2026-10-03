# Диагностика VPN на ПК (Windows). Сервер подставляет свои адреса при выдаче скрипта.
# Собирает: состояние Clash Verge (настройки, правила, журнал ядра — пароли скрыты), сеть ПК,
# мешающие программы, проверки сайтов и тест каждого протокола отдельно.
# Отчёт сохраняется на рабочий стол и отправляется на ваш VPN-сервер.
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'
try { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls13 } catch { [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 }
$Server = '__SERVER__'; $SubUrl = '__SUB__'; $UpUrl = '__UP__'; $DiagProfile = '__DIAGPROFILE__'
$UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36'
$Report = New-Object System.Text.StringBuilder
function Log([string]$s) { [void]$Report.AppendLine($s); Write-Host $s }
function Sec([string]$t) { Log ''; Log ('===== ' + $t + ' =====') }
function Safe([string]$name, [scriptblock]$b) { try { & $b } catch { Log ('  [' + $name + '] ошибка: ' + $_.Exception.Message) } }
function Redact([string]$s) {
    $s = [regex]::Replace($s, '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}', '<uuid>')
    [regex]::Replace($s, '(?im)^(\s*-?\s*(password|obfs-password|public-key|short-id|secret|uuid)\s*:\s*).*$', '$1<скрыто>')
}
function Stats($arr) {
    if (-not $arr -or $arr.Count -eq 0) { return 'нет данных' }
    $s = $arr | Sort-Object
    '{0:N0}/{1:N0}/{2:N0} мс (мин/медиана/макс)' -f $s[0], $s[[int][math]::Floor($s.Count / 2)], $s[-1]
}
# HTTP-запрос: $proxy=$null — как ходит система (TUN или системный прокси), иначе через указанный прокси
function Http([string]$url, $proxy) {
    $sw = [Diagnostics.Stopwatch]::StartNew()
    try {
        $p = @{ Uri = $url; UseBasicParsing = $true; TimeoutSec = 12; UserAgent = $UA; MaximumRedirection = 5; ErrorAction = 'Stop' }
        if ($proxy) { $p.Proxy = $proxy }
        $r = Invoke-WebRequest @p
        return @{ Text = ('{0} за {1} мс' -f [int]$r.StatusCode, $sw.ElapsedMilliseconds); Body = $r.Content }
    } catch {
        $code = $null
        if ($_.Exception.Response) { try { $code = [int]$_.Exception.Response.StatusCode } catch {} }
        if ($code) { return @{ Text = ('{0} за {1} мс' -f $code, $sw.ElapsedMilliseconds); Body = '' } }
        return @{ Text = ('НЕ ОТКРЫЛОСЬ за {0} мс: {1}' -f $sw.ElapsedMilliseconds, $_.Exception.Message); Body = '' }
    }
}
# скорость загрузки за ограниченное время (медленная линия не растягивает тест); $proxy=$null — без прокси
function Speed([string]$url, $proxy, [int]$sec = 8) {
    $req = [Net.HttpWebRequest]::Create($url); $req.Timeout = 15000; $req.ReadWriteTimeout = 15000; $req.UserAgent = $UA
    if ($proxy) { $req.Proxy = New-Object Net.WebProxy($proxy) } else { $req.Proxy = $null }
    $resp = $req.GetResponse(); $st = $resp.GetResponseStream(); $buf = New-Object byte[] 262144; $total = 0
    $sw = [Diagnostics.Stopwatch]::StartNew()
    while ($sw.Elapsed.TotalSeconds -lt $sec) { $n = $st.Read($buf, 0, $buf.Length); if ($n -le 0) { break }; $total += $n }
    $t = $sw.Elapsed.TotalSeconds; $resp.Close()
    [math]::Round($total * 8 / $t / 1e6)
}
$Sites = [ordered]@{
    'Выход в интернет'       = 'https://www.cloudflare.com/cdn-cgi/trace'
    'YouTube'                = 'https://www.youtube.com/'
    'YouTube: видеосервер'   = 'https://redirector.googlevideo.com/report_mapping?di=no'
    'Apple'                  = 'https://www.apple.com/'
    'App Store'              = 'https://apps.apple.com/'
    'TikTok'                 = 'https://www.tiktok.com/'
    'Instagram'              = 'https://www.instagram.com/'
    'Telegram'               = 'https://web.telegram.org/'
    'Discord'                = 'https://discord.com/'
    'Яндекс (напрямую)'      = 'https://ya.ru/'
    'Госуслуги (напрямую)'   = 'https://www.gosuslugi.ru/'
}
function TestSites($proxy) {
    foreach ($k in $Sites.Keys) {
        $r = Http $Sites[$k] $proxy
        $extra = ''
        if ($k -eq 'Выход в интернет' -and $r.Body) { $extra = '  ' + ((($r.Body -split "`n") | Where-Object { $_ -match '^(ip|loc|colo)=' }) -join ' ') }
        if ($k -eq 'YouTube: видеосервер' -and $r.Body) { $extra = '  ' + ($r.Body -split "`n")[0].Trim() }
        Log ('  {0,-22} {1}{2}' -f $k, $r.Text, $extra)
    }
}

Write-Host ''
Write-Host 'Диагностика VPN. Займёт 2-4 минуты. Отчёт уйдёт на ваш сервер и сохранится на рабочий стол.' -ForegroundColor Cyan
Log ('Отчёт диагностики VPN  ' + (Get-Date -Format 'yyyy-MM-dd HH:mm:ss zzz'))

Sec 'Система'
Safe 'os' { $os = Get-CimInstance Win32_OperatingSystem; Log ('  ' + $os.Caption + ' ' + $os.Version + ' ' + $os.OSArchitecture + ', PowerShell ' + $PSVersionTable.PSVersion) }

Sec 'Clash Verge'
$VergeDir = Join-Path $env:APPDATA 'io.github.clash-verge-rev.clash-verge-rev'
$Core = $null
Safe 'процессы' {
    Get-Process | Where-Object { $_.ProcessName -match 'verge|mihomo|clash' } | ForEach-Object {
        $path = ''; try { $path = $_.Path } catch {}
        if ($_.ProcessName -like 'verge-mihomo*' -and $path) { $script:Core = $path }
        Log ('  запущен: {0} (PID {1}) {2}' -f $_.ProcessName, $_.Id, $path)
    }
}
if (-not $Core) {
    foreach ($d in @("$env:ProgramFiles\Clash Verge", "${env:ProgramFiles(x86)}\Clash Verge", "$env:LOCALAPPDATA\Clash Verge", "$env:LOCALAPPDATA\Programs\Clash Verge")) {
        $f = Get-ChildItem -Path $d -Filter 'verge-mihomo*.exe' -ErrorAction SilentlyContinue | Sort-Object Name | Select-Object -First 1
        if ($f) { $Core = $f.FullName; break }
    }
}
Safe 'ядро' { if ($Core) { Log ('  ядро: ' + $Core + '  ' + ((& $Core -v 2>&1 | Select-Object -First 1) -join '')) } else { Log '  ядро verge-mihomo.exe не найдено' } }
Safe 'файлы' {
    if (-not (Test-Path $VergeDir)) { Log ('  папка настроек не найдена: ' + $VergeDir); return }
    foreach ($n in @('verge.yaml', 'profiles.yaml')) {
        $f = Join-Path $VergeDir $n
        if (Test-Path $f) { Log ('  --- ' + $n); (Redact ((Get-Content $f -Encoding UTF8 -TotalCount 200) -join "`n")) -split "`n" | ForEach-Object { Log ('    ' + $_) } }
    }
    $rt = Join-Path $VergeDir 'clash-verge.yaml'
    if (Test-Path $rt) {
        Log '  --- clash-verge.yaml (итоговая конфигурация, которую реально получило ядро)'
        (Redact ((Get-Content $rt -Encoding UTF8 -TotalCount 400) -join "`n")) -split "`n" | ForEach-Object { Log ('    ' + $_) }
    }
    $pd = Join-Path $VergeDir 'profiles'
    if (Test-Path $pd) {
        Get-ChildItem $pd -File | ForEach-Object {
            Log ('  профиль/расширение: {0} ({1} байт, изменён {2})' -f $_.Name, $_.Length, $_.LastWriteTime)
            if (($_.Extension -eq '.js' -or $_.Name -match '^m') -and $_.Length -lt 20000) {
                (Get-Content $_.FullName -Encoding UTF8 -TotalCount 60) | Where-Object { $_ -notmatch '^\s*(#|//)' -and $_.Trim() } | ForEach-Object { Log ('      ' + $_) }
            }
        }
    }
}
Safe 'журнал' {
    $logs = Get-ChildItem (Join-Path $VergeDir 'logs') -Recurse -File -Include *.log -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 3
    foreach ($l in $logs) {
        Log ('  --- журнал ' + $l.FullName.Replace($VergeDir, '') + ' (' + $l.LastWriteTime + ')')
        $lines = Get-Content $l.FullName -Encoding UTF8 -Tail 400
        $bad = $lines | Where-Object { $_ -match 'error|warn|fail|timeout|refused|reset|reality|invalid' } | Select-Object -Last 60
        foreach ($x in $bad) { Log ('    ' + (Redact $x)) }
        foreach ($x in ($lines | Select-Object -Last 15)) { Log ('    > ' + (Redact $x)) }
    }
}

Sec 'Сеть ПК'
Safe 'адаптеры' { Get-NetAdapter | Where-Object Status -eq 'Up' | ForEach-Object { Log ('  {0} [{1}] {2}' -f $_.Name, $_.InterfaceDescription, $_.LinkSpeed) } }
Safe 'адреса' { Get-NetIPAddress | Where-Object { $_.IPAddress -notmatch '^(fe80|169\.254\.|127\.|::1$)' } | ForEach-Object { Log ('  {0,-42} {1}' -f $_.IPAddress, $_.InterfaceAlias) } }
Safe 'маршруты' { Get-NetRoute -DestinationPrefix '0.0.0.0/0', '::/0' -ErrorAction SilentlyContinue | Sort-Object { $_.RouteMetric + $_.InterfaceMetric } | ForEach-Object { Log ('  маршрут {0,-10} -> {1,-16} {2} (метрика {3})' -f $_.DestinationPrefix, $_.NextHop, $_.InterfaceAlias, ($_.RouteMetric + $_.InterfaceMetric)) } }
Safe 'dns' { Get-DnsClientServerAddress | Where-Object { $_.ServerAddresses } | ForEach-Object { Log ('  DNS {0}: {1}' -f $_.InterfaceAlias, ($_.ServerAddresses -join ', ')) } }
Safe 'doh' { $d = Get-DnsClientDohServerAddress -ErrorAction SilentlyContinue; foreach ($x in $d) { Log ('  DoH в Windows: {0} {1}' -f $x.ServerAddress, $x.DohTemplate) } }
Safe 'tcp' {
    # автонастройка окна приёма: если disabled/highlyrestricted — загрузка с дальних серверов упирается в ~10-30 Мбит/с
    Get-NetTCPSetting -ErrorAction SilentlyContinue | Where-Object { $_.SettingName -in 'Internet', 'InternetCustom', 'Automatic' } | ForEach-Object {
        Log ('  TCP {0}: автонастройка окна={1}, алгоритм={2}, метки времени={3}, эвристика={4}' -f $_.SettingName, $_.AutoTuningLevelLocal, $_.CongestionProvider, $_.Timestamps, $_.ScalingHeuristics)
    }
}
Safe 'прокси' { $ie = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'; Log ('  системный прокси: включён={0} сервер={1} PAC={2}' -f $ie.ProxyEnable, $ie.ProxyServer, $ie.AutoConfigURL) }

Sec 'Программы, которые могут мешать VPN'
$pat = 'winws|goodbyedpi|zapret|byedpi|ciadpi|spoofdpi|nekoray|nekobox|throne|v2rayn|xray|sing-box|happ|hiddify|amnezia|wireguard|openvpn|outline|psiphon|warp|windscribe|proton|nordvpn|expressvpn|avp|kaspersky|ekrn|drweb|adguard|avast|avg|netlimiter|clumsy|proxifier'
Safe 'процессы' {
    $p = Get-Process | Where-Object { $_.ProcessName -match $pat } | Select-Object -ExpandProperty ProcessName -Unique
    if ($p) { Log ('  процессы: ' + ($p -join ', ')) } else { Log '  подозрительных процессов нет' }
    if ($p -match 'winws') {
        $zc = @(Get-CimInstance Win32_Process -Filter "Name='winws.exe'" | ForEach-Object { $_.CommandLine }) -join ' '
        if (-not $zc) { try { $zc = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Services\zapret').ImagePath } catch {} }
        if ($zc -match '--wf-iface' -and $zc -match [regex]::Escape($Server)) { Log '  zapret настроен для работы с VPN (только реальный адаптер, сервер исключён) — ок' }
        else { Log '  !!! zapret НЕ настроен для работы с VPN: он лезет в туннель Clash и ломает YouTube, Discord, Telegram. Запустите скрипт zapret-fix.' }
    } elseif ($p -match 'goodbyedpi|byedpi|ciadpi|spoofdpi') { Log '  !!! Работает другой обходчик блокировок — вместе с VPN он ломает сайты из своих списков. Выключите его.' }
    if ($p -match 'happ|v2rayn|nekoray|nekobox|throne|hiddify|amnezia|wireguard|openvpn|outline|warp|sing-box|xray') { Log '  !!! Запущен другой VPN-клиент. Два VPN одновременно конфликтуют — оставьте один (Clash Verge).' }
}
Safe 'службы' { Get-Service | Where-Object { ($_.Name + ' ' + $_.DisplayName) -match ($pat + '|windivert') -and $_.Status -eq 'Running' } | ForEach-Object { Log ('  служба работает: {0} ({1})' -f $_.Name, $_.DisplayName) } }
Safe 'zapret' { Get-CimInstance Win32_Service | Where-Object { $_.PathName -match 'winws' } | ForEach-Object { Log ('  zapret-служба ' + $_.Name + ' (' + $_.State + '): ' + $_.PathName) } }
Safe 'windivert' { foreach ($n in 'WinDivert', 'WinDivert14', 'WinDivert1.4') { $q = sc.exe query $n 2>$null | Select-String 'STATE'; if ($q) { Log ('  драйвер {0}: {1}' -f $n, $q.ToString().Trim()) } } }
Safe 'туннели' { Get-NetAdapter -IncludeHidden | Where-Object { $_.InterfaceDescription -match 'Wintun|TAP|WireGuard|Amnezia|Meta|Mihomo|Clash|sing|OpenVPN|Tunnel' } | ForEach-Object { Log ('  туннельный адаптер: {0} [{1}] {2}' -f $_.Name, $_.InterfaceDescription, $_.Status) } }

Sec 'Проверка как есть (так открывают сайты браузер и приложения)'
Safe 'dns-запросы' {
    foreach ($h in 'www.youtube.com', 'www.apple.com', 'www.tiktok.com', 'ya.ru') {
        foreach ($t in 'A', 'AAAA') {
            $r = Resolve-DnsName -Name $h -Type $t -DnsOnly -QuickTimeout -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress }
            Log ('  DNS {0,-16} {1,-4} {2}' -f $h, $t, (($r | ForEach-Object { $_.IPAddress }) -join ', '))
        }
    }
}
Safe 'ipv6' { $r = Http 'https://api6.ipify.org' $null; Log ('  IPv6-интернет: ' + $r.Text + ' ' + $r.Body) }
Safe 'сайты' { TestSites $null }
$Mixed = $false
Safe 'порт 7897' { $c = New-Object Net.Sockets.TcpClient; $script:Mixed = $c.ConnectAsync('127.0.0.1', 7897).Wait(1000); $c.Close() }
if ($Mixed) { Sec 'Проверка через порт Clash Verge 127.0.0.1:7897 (минуя TUN)'; Safe 'сайты-прокси' { TestSites 'http://127.0.0.1:7897' } }

Sec 'Тест протоколов отдельно от настроек Clash Verge'
$skip = $false
if (-not $Core) { Log '  ядро не найдено — тест пропущен'; $skip = $true }
elseif (Get-Process -Name 'clash-verge*', 'verge-mihomo*' -ErrorAction SilentlyContinue) {
    Write-Host ''
    Write-Host 'Сейчас закройте Clash Verge: значок в трее (у часов) -> правый клик -> Выход / Quit.' -ForegroundColor Yellow
    Write-Host 'Потом нажмите Enter. Чтобы пропустить этот тест — введите s и Enter.' -ForegroundColor Yellow
    if ((Read-Host) -eq 's') { $skip = $true; Log '  тест пропущен пользователем' }
    elseif (Get-Process -Name 'verge-mihomo*' -ErrorAction SilentlyContinue) { Log '  ВНИМАНИЕ: ядро Clash Verge всё ещё работает — результаты могут быть искажены' }
}
if (-not $skip) {
    $T = Join-Path $env:TEMP 'vpn-diag'
    Remove-Item $T -Recurse -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory $T -Force | Out-Null
    Get-ChildItem $VergeDir -File -ErrorAction SilentlyContinue | Where-Object { $_.Extension -in '.dat', '.mmdb', '.metadb' } | Copy-Item -Destination $T
    $Api = 'http://127.0.0.1:19097'; $Px = 'http://127.0.0.1:17897'; $proc = $null
    Safe 'запуск ядра' {
        $w = New-Object Net.WebClient
        [IO.File]::WriteAllBytes((Join-Path $T 'config.yaml'), $w.DownloadData($DiagProfile))
        $script:proc = Start-Process -FilePath $Core -ArgumentList ('-d "{0}" -f "{1}"' -f $T, (Join-Path $T 'config.yaml')) -NoNewWindow -PassThru -RedirectStandardOutput (Join-Path $T 'core.log') -RedirectStandardError (Join-Path $T 'core.err')
        $ok = $false
        for ($i = 0; $i -lt 60 -and -not $ok; $i++) { Start-Sleep -Milliseconds 500; try { Invoke-RestMethod "$Api/version" -TimeoutSec 2 | Out-Null; $ok = $true } catch {} }
        if (-not $ok) { Stop-Process -Id $script:proc.Id -Force -ErrorAction SilentlyContinue; $script:proc = $null; throw 'ядро не запустилось за 30 с' }
        Log '  тестовое ядро запущено с чистым профилем (без TUN и без настроек Clash Verge)'
    }
    if ($proc) {
        $all = $null; try { $w = New-Object Net.WebClient; $w.Encoding = [Text.Encoding]::UTF8; $all = ($w.DownloadString("$Api/proxies") | ConvertFrom-Json).proxies } catch { Log ('  API ядра недоступно: ' + $_.Exception.Message) }
    }
    if ($proc -and $all) {
        $names = @($all.PSObject.Properties | Where-Object { $_.Value.type -in 'Vless', 'Hysteria2' } | ForEach-Object { $_.Name })
        function Delay([string]$name) {
            try { (Invoke-RestMethod ("$Api/proxies/" + [uri]::EscapeDataString($name) + '/delay?timeout=5000&url=' + [uri]::EscapeDataString('https://www.gstatic.com/generate_204')) -TimeoutSec 8).delay } catch { -1 }
        }
        function Pick([string]$group, [string]$name) {
            Invoke-RestMethod -Method Put ("$Api/proxies/" + [uri]::EscapeDataString($group)) -Body ([Text.Encoding]::UTF8.GetBytes((@{ name = $name } | ConvertTo-Json))) -ContentType 'application/json' | Out-Null
        }
        $grpVpn = @($all.PSObject.Properties | Where-Object { $_.Value.type -eq 'Selector' -and $_.Name -match 'VPN' } | ForEach-Object { $_.Name })[0]
        $grpGame = @($all.PSObject.Properties | Where-Object { $_.Value.type -eq 'Selector' -and $_.Name -match 'Игры' } | ForEach-Object { $_.Name })[0]
        foreach ($n in $names) {
            Log ''; Log ('  --- ' + $n)
            Safe 'задержка' {
                $d = @(); $fail = 0
                for ($i = 0; $i -lt 20; $i++) { $x = Delay $n; if ($x -gt 0) { $d += $x } else { $fail++ } }
                Log ('  20 новых подключений: удачных {0}, неудачных {1}; задержка {2}' -f $d.Count, $fail, (Stats $d))
            }
            Safe 'скорость' {
                Pick $grpVpn $n
                $dn = Speed 'https://speed.cloudflare.com/__down?bytes=50000000' $Px
                $w = New-Object Net.WebClient; $w.Proxy = New-Object Net.WebProxy($Px)
                $buf = New-Object byte[] 10000000
                $sw = [Diagnostics.Stopwatch]::StartNew(); [void]$w.UploadData('https://speed.cloudflare.com/__up', 'POST', $buf); $upl = $buf.Length * 8 / $sw.Elapsed.TotalSeconds / 1e6
                Log ('  скорость: загрузка {0:N0} Мбит/с, отдача {1:N0} Мбит/с' -f $dn, $upl)
            }
            if (@($all.$grpGame.all) -notcontains $n) { continue }
            Safe 'udp' {
                Pick $grpGame $n
                $tcp = New-Object Net.Sockets.TcpClient('127.0.0.1', 17897); $s = $tcp.GetStream(); $s.ReadTimeout = 5000
                $h = New-Object byte[] 32
                $s.Write([byte[]](5, 1, 0), 0, 3); [void]$s.Read($h, 0, 2)
                $s.Write([byte[]](5, 3, 0, 1, 0, 0, 0, 0, 0, 0), 0, 10); [void]$s.Read($h, 0, 10)
                $u = New-Object Net.Sockets.UdpClient; $u.Client.ReceiveTimeout = 1500; $u.Connect('127.0.0.1', $h[8] * 256 + $h[9])
                $q = [byte[]]((0, 0, 0, 1, 1, 1, 1, 1, 0, 53, 0, 0, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7) + [Text.Encoding]::ASCII.GetBytes('example') + 3 + [Text.Encoding]::ASCII.GetBytes('com') + (0, 0, 1, 0, 1))
                $rtt = @(); $lost = 0
                for ($i = 0; $i -lt 100; $i++) {
                    $q[10] = [byte]($i -shr 8); $q[11] = [byte]($i -band 255)
                    $sw = [Diagnostics.Stopwatch]::StartNew(); [void]$u.Send($q, $q.Length)
                    try { $ep = New-Object Net.IPEndPoint([Net.IPAddress]::Any, 0); [void]$u.Receive([ref]$ep); $rtt += $sw.Elapsed.TotalMilliseconds } catch { $lost++ }
                    Start-Sleep -Milliseconds 40
                }
                $u.Close(); $tcp.Close()
                $spikes = @($rtt | Where-Object { $_ -gt 150 }).Count
                Log ('  UDP как в игре (100 пакетов): потеряно {0}, задержка {1}, выбросов >150 мс: {2}' -f $lost, (Stats $rtt), $spikes)
            }
        }
        Log ''; Log '  --- сайты через тестовое ядро (группа VPN = Авто)'
        Safe 'авто' { Pick $grpVpn (@($all.$grpVpn.all)[0]); TestSites $Px }
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep -Milliseconds 500
        Safe 'журнал теста' { Get-Content (Join-Path $T 'core.log') -Encoding UTF8 -ErrorAction SilentlyContinue | Where-Object { $_ -match 'level=(warn|error)' } | Select-Object -Last 40 | ForEach-Object { Log ('    ' + (Redact $_)) } }
    }
    # Clash Verge закрыт -> запросы идут мимо VPN, через один только zapret: так видно, рабочая ли стратегия zapret
    Sec 'Напрямую без VPN (только zapret)'
    if (Get-Process -Name 'verge-mihomo*' -ErrorAction SilentlyContinue) { Log '  ВНИМАНИЕ: Clash Verge не закрыт — эти запросы всё равно идут через него' }
    Safe 'zapret-сайты' {
        $direct = [ordered]@{
            'YouTube'              = 'https://www.youtube.com/generate_204'
            'YouTube: видеосервер' = 'https://redirector.googlevideo.com/report_mapping?di=no'
            'Discord'              = 'https://discord.com/api/v9/gateway'
            'Twitch'               = 'https://www.twitch.tv/robots.txt'
        }
        foreach ($k in $direct.Keys) { $r = Http $direct[$k] $null; Log ('  {0,-22} {1}' -f $k, $r.Text) }
    }
    # откуда тормозит загрузка: сравниваем без VPN — Москва (Cloudflare), наш сервер обычным HTTPS, Европа (хостинг и провайдер)
    $speeds = [ordered]@{
        'Cloudflare (Москва)'                = 'https://speed.cloudflare.com/__down?bytes=50000000'
        'наш сервер без VPN (обычный HTTPS)' = ($SubUrl + '/speed?bytes=100000000')
        'OVH, Франция (хостинг)'             = 'https://proof.ovh.net/files/100Mb.dat'
        'Tele2, Швеция (провайдер)'          = 'http://speedtest.tele2.net/100MB.zip'
    }
    foreach ($k in $speeds.Keys) { Safe $k { Log ('  загрузка без VPN, {0,-36} {1} Мбит/с' -f $k, (Speed $speeds[$k] $null)) } }
    Sec 'Связь с сервером напрямую'
    Safe 'ping' {
        # PowerShell 5.1: StatusCode/ResponseTime, PowerShell 7: Status/Latency
        $p = @(Test-Connection -ComputerName $Server -Count 20 -ErrorAction SilentlyContinue | Where-Object { $_.StatusCode -eq 0 -or "$($_.Status)" -eq 'Success' })
        $t = @($p | ForEach-Object { if ($null -ne $_.ResponseTime) { [double]$_.ResponseTime } else { [double]$_.Latency } })
        Log ('  ping {0}: ответов {1}/20, {2}' -f $Server, $p.Count, (Stats $t))
    }
    Safe 'tcp443' {
        $t = @(); $fail = 0
        for ($i = 0; $i -lt 10; $i++) { $c = New-Object Net.Sockets.TcpClient; $sw = [Diagnostics.Stopwatch]::StartNew(); if ($c.ConnectAsync($Server, 443).Wait(3000)) { $t += $sw.Elapsed.TotalMilliseconds } else { $fail++ }; $c.Close() }
        Log ('  TCP 443: неудачных {0}/10, {1}' -f $fail, (Stats $t))
    }
    Write-Host ''
    Write-Host 'Тест протоколов окончен — можно снова запустить Clash Verge.' -ForegroundColor Green
}

$text = $Report.ToString()
$desk = [Environment]::GetFolderPath('Desktop'); if (-not $desk -or -not (Test-Path $desk)) { $desk = $env:TEMP }
$file = Join-Path $desk ('vpn-diag-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.txt')
Safe 'сохранение' { [IO.File]::WriteAllText($file, $text, [Text.Encoding]::UTF8) }
try {
    $w = New-Object Net.WebClient; $w.Headers['Content-Type'] = 'text/plain; charset=utf-8'
    [void]$w.UploadData($UpUrl, 'POST', [Text.Encoding]::UTF8.GetBytes($text))
    Write-Host ('Готово. Отчёт отправлен на сервер и сохранён: ' + $file) -ForegroundColor Green
} catch {
    Write-Host ('Отчёт сохранён: ' + $file + ' (отправить не удалось: ' + $_.Exception.Message + ')') -ForegroundColor Yellow
}
