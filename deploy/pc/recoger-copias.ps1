# Recoge en el PC las copias de seguridad cifradas del homeserver.
#
# Trae por SSH (Tailscale o, si no, la red local) las copias nuevas de
# ~/srv/backups/copias, deja en el móvil la marca .recogida (el panel la
# enseña y avisa si pasan días sin ella) y guarda en el PC las 14 más
# recientes y la última de cada mes durante un año.
#
# Lo lanza una tarea programada de Windows al iniciar sesión y cada 4 horas
# (deploy/pc/instalar-tarea.ps1). Usa los alias de ~/.ssh/config (xperia-ts,
# xperia), con la clave de siempre y sin pedir nada.
param(
	[string]$Destino = (Join-Path $env:USERPROFILE "Documents\xperia\copias"),
	[string[]]$Hosts = @("xperia-ts", "xperia")
)
$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force $Destino | Out-Null
$log = Join-Path $Destino "recogida.log"

function Log([string]$msg) {
	$line = "{0:yyyy-MM-dd HH:mm:ss} {1}" -f (Get-Date), $msg
	Add-Content -Path $log -Value $line -Encoding UTF8
	Write-Output $line
}

$sshOpts = @("-o", "BatchMode=yes", "-o", "ConnectTimeout=10")
$remoteDir = "srv/backups/copias"

# Primer alias que responde.
$hostOk = $null
$remote = @()
foreach ($h in $Hosts) {
	$out = & ssh @sshOpts $h "ls -1 $remoteDir 2>/dev/null" 2>$null
	if ($LASTEXITCODE -eq 0) {
		$hostOk = $h
		$remote = @($out | Where-Object { $_ -match '^homeserver-\d{8}-\d{4}\.tar\.gz\.age$' })
		break
	}
}
if (-not $hostOk) {
	Log "El móvil no responde por ningún camino ($($Hosts -join ', ')); se reintentará."
	exit 1
}

$fallos = 0
$nuevas = 0
foreach ($f in $remote) {
	$dst = Join-Path $Destino $f
	if (Test-Path $dst) { continue }
	$part = "$dst.part"
	& scp @sshOpts -q "${hostOk}:$remoteDir/$f" $part
	if ($LASTEXITCODE -eq 0) {
		Move-Item -Force $part $dst
		$nuevas++
	} else {
		Remove-Item -ErrorAction SilentlyContinue $part
		$fallos++
		Log "No se pudo traer $f."
	}
}

if ($fallos -eq 0) {
	& ssh @sshOpts $hostOk "touch $remoteDir/.recogida" | Out-Null
}

# En el PC: las 14 más recientes y la más reciente de cada uno de los últimos 12 meses.
$locales = Get-ChildItem -Path $Destino -Filter "homeserver-*.tar.gz.age" | Sort-Object Name -Descending
$keep = @{}
$meses = @{}
$i = 0
foreach ($c in $locales) {
	if ($i -lt 14) { $keep[$c.Name] = $true }
	$mes = $c.Name.Substring(11, 6) # homeserver-AAAAMM…
	if (-not $meses.ContainsKey($mes) -and $meses.Count -lt 12) {
		$meses[$mes] = $true
		$keep[$c.Name] = $true
	}
	$i++
}
foreach ($c in $locales) {
	if (-not $keep.ContainsKey($c.Name)) { Remove-Item $c.FullName }
}

# El registro no crece sin fin.
$lines = Get-Content $log -Encoding UTF8 -ErrorAction SilentlyContinue
if ($lines.Count -gt 500) { $lines | Select-Object -Last 300 | Set-Content $log -Encoding UTF8 }

Log "Recogida por ${hostOk}: $nuevas nuevas, $fallos fallos, $($keep.Count) guardadas en el PC."
if ($fallos -gt 0) { exit 1 }
