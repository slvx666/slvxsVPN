# Установка «игрового режима Wi-Fi» (Windows, от администратора). Сервер подставляет свой адрес для отчёта.
# Фоновое задание Windows (без окон, от имени системы): пока запущена игра — Windows не сканирует Wi-Fi
# (именно сканирование даёт заморозки по 0,5-3 с), игра закрыта — всё как обычно. Если Wi-Fi во время игры
# отвалился — сканирование сразу разрешается, чтобы он переподключился.
# Дополнительно: запрещает Windows отключать Wi-Fi-адаптер для экономии энергии (через реестр).
# Удаление: запустить эту же команду ещё раз и выбрать «удалить».
$ErrorActionPreference = 'Continue'
$UpUrl = '__UP__'
$Dir = Join-Path $env:ProgramData 'VPN-GameMode'; $Task = 'VPN Game Mode'
$Log = New-Object System.Text.StringBuilder
function Say([string]$s, [string]$c = 'Gray') { [void]$Log.AppendLine($s); Write-Host $s -ForegroundColor $c }
function Send {
    try {
        $w = New-Object Net.WebClient; $w.Headers['Content-Type'] = 'text/plain; charset=utf-8'
        [void]$w.UploadData($UpUrl, 'POST', [Text.Encoding]::UTF8.GetBytes('game-mode ' + (Get-Date -Format 's') + "`n" + $Log.ToString()))
    } catch {}
}
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { Write-Host 'Нужны права администратора: Win+X -> «Терминал (администратор)», затем вставьте команду ещё раз.' -ForegroundColor Yellow; return }

$installed = [bool](Get-ScheduledTask -TaskName $Task -ErrorAction SilentlyContinue)
if ($installed) {
    $ans = (Read-Host 'Игровой режим уже установлен. 1 — переустановить (обновить), 2 — удалить, Enter — ничего не делать').Trim()
    if ($ans -eq '2') {
        Unregister-ScheduledTask -TaskName $Task -Confirm:$false -ErrorAction SilentlyContinue
        Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" | Where-Object { $_.CommandLine -match 'VPN-GameMode' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
        Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.NdisPhysicalMedium -eq 9 } | ForEach-Object { netsh wlan set autoconfig enabled=yes interface="$($_.Name)" | Out-Null }
        Remove-Item $Dir -Recurse -Force -ErrorAction SilentlyContinue
        Say 'Игровой режим удалён, сканирование Wi-Fi включено.' 'Green'; Send; return
    }
    if ($ans -ne '1') { Say 'Ничего не изменено.'; return }
}

# --- сторож: проверяет раз в 5 с, запущена ли игра
$watcher = @'
# Игровой режим Wi-Fi: пока запущена игра — Windows не сканирует эфир; нет игры — всё как обычно.
$ErrorActionPreference = 'SilentlyContinue'
$Dir = Join-Path $env:ProgramData 'VPN-GameMode'; $LogFile = Join-Path $Dir 'log.txt'; $Extra = Join-Path $Dir 'extra.txt'
# игры — по папке установки (любые игры магазинов) и по имени процесса (известные игры вне этих папок)
$paths = '\\steamapps\\common\\|\\Epic Games\\|\\Riot Games\\|\\EA Games\\|\\Origin Games\\|\\Ubisoft Game Launcher\\games\\|\\GOG Galaxy\\Games\\|\\XboxGames\\|\\Games\\'
$names = '^(r5apex|r5apex_dx12|cs2|dota2|VALORANT-Win64-Shipping|FortniteClient-Win64-Shipping|RainbowSix|TslGame|EscapeFromTarkov|GTA5|RDR2|Overwatch|League of Legends|Warface|WorldOfTanks|WorldOfWarships|DeadByDaylight-Win64-Shipping|eldenring|Cyberpunk2077|Minecraft|javaw|FiveM|RobloxPlayerBeta)$'
# лаунчеры и служебные программы — не игры (иначе режим был бы включён постоянно)
$skip = '(?i)wallpaper|Steamworks Shared|redist|crash|launcher|EasyAntiCheat|BEService|unins|\\Steam\\(?!steamapps\\)|Riot Client|Battle\.net|EA Desktop|GalaxyClient|UbisoftConnect|\\upc\.exe|Overwolf|EpicOnlineServices|EpicWebHelper|UnrealCEFSubProcess'
function Say([string]$s) {
    try { if ((Get-Item $LogFile).Length -gt 1MB) { Remove-Item $LogFile } } catch {}
    Add-Content -Path $LogFile -Value ((Get-Date -Format 'yyyy-MM-dd HH:mm:ss') + '  ' + $s) -Encoding UTF8
}
function WifiAdapters { @(Get-NetAdapter -Physical | Where-Object { $_.NdisPhysicalMedium -eq 9 }) }
function SetScan([bool]$on) { foreach ($a in (WifiAdapters)) { netsh wlan set autoconfig enabled=$(if ($on) { 'yes' } else { 'no' }) interface="$($a.Name)" | Out-Null } }
SetScan $true; $state = 'on'; Say 'сторож запущен, сканирование Wi-Fi включено'
while ($true) {
    $extra = @(); if (Test-Path $Extra) { $extra = @(Get-Content $Extra | ForEach-Object { $_.Trim() -replace '\.exe$', '' } | Where-Object { $_ -and $_ -notmatch '^#' }) }
    $game = Get-Process | Where-Object {
        ($_.Path -and $_.Path -match $paths -and $_.Path -notmatch $skip) -or $_.ProcessName -match $names -or ($extra -contains $_.ProcessName)
    } | Select-Object -First 1
    $connected = @(WifiAdapters | Where-Object { $_.Status -eq 'Up' }).Count -gt 0
    if ($game -and $connected -and $state -ne 'off') { SetScan $false; $state = 'off'; Say ("игра: " + $game.ProcessName + " — сканирование Wi-Fi выключено") }
    elseif ($game -and -not $connected -and $state -eq 'off') { SetScan $true; $state = 'on'; Say 'Wi-Fi отключился во время игры — сканирование включено для переподключения' }
    elseif (-not $game -and $state -ne 'on') { SetScan $true; $state = 'on'; Say 'игр нет — сканирование Wi-Fi включено' }
    Start-Sleep -Seconds 5
}
'@
New-Item -ItemType Directory -Path $Dir -Force | Out-Null
[IO.File]::WriteAllText((Join-Path $Dir 'watcher.ps1'), $watcher, (New-Object Text.UTF8Encoding $true))
$ex = Join-Path $Dir 'extra.txt'
if (-not (Test-Path $ex)) {
    [IO.File]::WriteAllText($ex, "# Дополнительные программы для игрового режима — по одной на строку, имя процесса (как в диспетчере задач).`r`n# Например, чтобы включать игровой режим во время звонков в Discord, уберите # в строке ниже:`r`n# Discord`r`n", (New-Object Text.UTF8Encoding $true))
}
Unregister-ScheduledTask -TaskName $Task -Confirm:$false -ErrorAction SilentlyContinue
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" | Where-Object { $_.CommandLine -match 'VPN-GameMode' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
$act = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument ('-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File "' + (Join-Path $Dir 'watcher.ps1') + '"')
$trg = New-ScheduledTaskTrigger -AtStartup
$pri = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$set = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable
Register-ScheduledTask -TaskName $Task -Action $act -Trigger $trg -Principal $pri -Settings $set -Description 'Во время игр отключает фоновое сканирование Wi-Fi (убирает заморозки), после — включает' | Out-Null
Start-ScheduledTask -TaskName $Task
Start-Sleep -Seconds 3
$ok = (Get-ScheduledTask -TaskName $Task).State -eq 'Running'
Say ($(if ($ok) { 'Игровой режим установлен и работает: во время игр Wi-Fi не сканирует эфир.' } else { 'Задание создано, но не запустилось — перезагрузите ПК.' })) $(if ($ok) { 'Green' } else { 'Yellow' })

# --- запрет отключать Wi-Fi-адаптер для экономии энергии (галочка «Разрешить отключение этого устройства»)
$cls = 'HKLM:\SYSTEM\CurrentControlSet\Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}'
foreach ($a in @(Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object { $_.NdisPhysicalMedium -eq 9 })) {
    $k = Get-ChildItem $cls -ErrorAction SilentlyContinue | Where-Object { (Get-ItemProperty $_.PSPath -ErrorAction SilentlyContinue).NetCfgInstanceId -eq $a.InterfaceGuid } | Select-Object -First 1
    if ($k) { Set-ItemProperty -Path $k.PSPath -Name PnPCapabilities -Value 24 -Type DWord; Say ('Адаптеру «' + $a.Name + '» запрещено отключаться для экономии энергии (вступит в силу после перезагрузки).') 'Green' }
}
Say ('Журнал игрового режима: ' + (Join-Path $Dir 'log.txt') + '; свои программы — в ' + $ex)
Send
