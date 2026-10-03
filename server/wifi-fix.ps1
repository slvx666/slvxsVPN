# Настройка Wi-Fi под игры (Windows, от администратора). Сервер подставляет свой адрес для отчёта.
# Лечит «заморозки» по 0,5-3 с, когда адаптер уходит сканировать эфир или экономить энергию и копит пакеты:
#  1) схема питания: беспроводной адаптер — максимальная производительность, выборочная приостановка USB — выкл;
#  2) адаптеру запрещено отключаться для экономии энергии;
#  3) в дополнительных свойствах драйвера: роуминг — минимальный, энергосбережение/MIMO power save — выкл,
#     объединение пакетов (packet coalescing) — выкл.
# Показывает план, спрашивает подтверждение, в конце отправляет отчёт. Wi-Fi переподключится на несколько секунд.
$ErrorActionPreference = 'Continue'
$UpUrl = '__UP__'
$Log = New-Object System.Text.StringBuilder
function Say([string]$s, [string]$c = 'Gray') { [void]$Log.AppendLine($s); Write-Host $s -ForegroundColor $c }
function Send {
    try {
        $w = New-Object Net.WebClient; $w.Headers['Content-Type'] = 'text/plain; charset=utf-8'
        [void]$w.UploadData($UpUrl, 'POST', [Text.Encoding]::UTF8.GetBytes('wifi-fix ' + (Get-Date -Format 's') + "`n" + $Log.ToString()))
    } catch {}
}
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { Write-Host 'Нужны права администратора: Win+X -> «Терминал (администратор)», затем вставьте команду ещё раз.' -ForegroundColor Yellow; return }

$a = Get-NetAdapter -Physical -ErrorAction SilentlyContinue | Where-Object {
    $_.Status -eq 'Up' -and ($_.NdisPhysicalMedium -eq 9 -or $_.InterfaceDescription -match 'Wi-?Fi|Wireless|WLAN|802\.11')
} | Select-Object -First 1
if (-not $a) { Say 'Подключённый Wi-Fi-адаптер не найден (если вы по кабелю — эта настройка не нужна).' 'Yellow'; Send; return }
Say ('Адаптер: ' + $a.Name + ' [' + $a.InterfaceDescription + '], драйвер ' + $a.DriverVersion + ' от ' + $a.DriverDate + ' (' + $a.DriverProvider + ')')
foreach ($l in (netsh wlan show interfaces)) { if ($l -match '(Тип радиомодуля|Radio type|Канал|Channel|Сигнал|Signal|Диапазон|Band|Скорость приема|Receive rate)\s') { Say ('  ' + $l.Trim()) } }

# --- план изменений в свойствах драйвера
$props = @(Get-NetAdapterAdvancedProperty -Name $a.Name -ErrorAction SilentlyContinue)
Say ''; Say 'Дополнительные свойства драйвера (сейчас):'
foreach ($p in $props) { Say ('  {0} = {1}' -f $p.DisplayName, $p.DisplayValue) }
$rules = @(
    @{ Name = '(?i)roam';                                                      Want = '(?i)lowest|1\.\s|самый низк|минимал|^disable|откл' },
    @{ Name = '(?i)power ?sav|mimo power|energy|u-?apsd|энерго|экономи|smart ps'; Want = '(?i)^(disable|disabled|off|no smps|max(imum)? perf|откл|выкл)' },
    @{ Name = '(?i)packet coalescing|объединени';                               Want = '(?i)^(disable|disabled|off|откл|выкл)' },
    @{ Name = '(?i)background scan|фонов';                                     Want = '(?i)^(disable|disabled|off|откл|выкл)' }
)
$plan = @()
foreach ($p in $props) {
    foreach ($r in $rules) {
        if ($p.DisplayName -notmatch $r.Name) { continue }
        $v = @($p.ValidDisplayValues | Where-Object { $_ -match $r.Want }) | Select-Object -First 1
        if ($v -and $v -ne $p.DisplayValue) { $plan += [pscustomobject]@{ Prop = $p.DisplayName; From = $p.DisplayValue; To = $v } }
        break
    }
}
# --- схема питания: ищем подгруппы по GUID параметров (названия локализованы)
function PowerSub([string]$setting) {
    $sub = $null
    foreach ($l in (powercfg /qh SCHEME_CURRENT)) {
        if ($l -match '(Subgroup|подгрупп).*?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})') { $sub = $Matches[2] }
        if ($l -match $setting) { return $sub }
    }
    $null
}
$wifiPS = '12bbebe6-58d6-4636-95bb-3217ef867c1a'; $usbSS = '48e6b7a6-50f5-4782-a5d4-53bb8f07e226'
$subWifi = PowerSub $wifiPS; $subUsb = PowerSub $usbSS
$pm = Get-NetAdapterPowerManagement -Name $a.Name -ErrorAction SilentlyContinue

Say ''; Say 'План:' 'Cyan'
if ($subWifi) { Say '  схема питания: беспроводной адаптер -> максимальная производительность' 'Cyan' }
if ($subUsb) { Say '  схема питания: выборочная приостановка USB -> выключить' 'Cyan' }
if ($pm -and "$($pm.AllowComputerToTurnOffDevice)" -ne 'Disabled') { Say '  адаптер: запретить отключение для экономии энергии' 'Cyan' }
foreach ($x in $plan) { Say ('  драйвер: {0}: {1} -> {2}' -f $x.Prop, $x.From, $x.To) 'Cyan' }
Say ''
$answer = (Read-Host 'Применить? Wi-Fi переподключится на несколько секунд. Введите y (или н / да), любое другое — отмена').Trim().ToLower()
if ($answer -notin 'y', 'yes', 'н', 'д', 'да') { Say ('Отменено (ответ: «' + $answer + '»).'); Send; return }

foreach ($pair in @(@($subWifi, $wifiPS), @($subUsb, $usbSS))) {
    if (-not $pair[0]) { continue }
    powercfg /setacvalueindex SCHEME_CURRENT $pair[0] $pair[1] 0 | Out-Null
    powercfg /setdcvalueindex SCHEME_CURRENT $pair[0] $pair[1] 0 | Out-Null
}
powercfg /setactive SCHEME_CURRENT | Out-Null
Say 'Схема питания обновлена.'
try { Set-NetAdapterPowerManagement -Name $a.Name -AllowComputerToTurnOffDevice Disabled -NoRestart -ErrorAction Stop; Say 'Отключение адаптера для экономии энергии запрещено.' } catch { Say ('Экономию энергии адаптера через систему не изменить: ' + $_.Exception.Message) 'Yellow' }
foreach ($x in $plan) {
    try { Set-NetAdapterAdvancedProperty -Name $a.Name -DisplayName $x.Prop -DisplayValue $x.To -NoRestart -ErrorAction Stop; Say ('Изменено: ' + $x.Prop + ' = ' + $x.To) }
    catch { Say ('Не удалось изменить ' + $x.Prop + ': ' + $_.Exception.Message) 'Yellow' }
}
Say 'Перезапускаю адаптер...'
try { Restart-NetAdapter -Name $a.Name -Confirm:$false -ErrorAction Stop } catch {}
for ($i = 0; $i -lt 30; $i++) { Start-Sleep -Seconds 1; if ((Get-NetAdapter -Name $a.Name).Status -eq 'Up') { break } }
Start-Sleep -Seconds 3
Say ('Wi-Fi: ' + (Get-NetAdapter -Name $a.Name).Status) 'Green'
Say 'Готово. Лучшее решение для игр — кабель; если его нет — держите адаптер на удлинителе USB подальше от корпуса ПК, на 5 ГГц.' 'Green'
Send
