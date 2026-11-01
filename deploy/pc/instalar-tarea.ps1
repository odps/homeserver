# Crea (o rehace) la tarea programada de Windows que recoge las copias del
# homeserver: al iniciar sesión y cada 4 horas, oculta y sin permisos de
# administrador. Para quitarla: Unregister-ScheduledTask -TaskName "homeserver - recoger copias"
$script = Join-Path $PSScriptRoot "recoger-copias.ps1"
$action = New-ScheduledTaskAction -Execute "powershell.exe" `
	-Argument "-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$script`""
$triggers = @(
	(New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME),
	(New-ScheduledTaskTrigger -Once -At (Get-Date).Date -RepetitionInterval (New-TimeSpan -Hours 4))
)
# Si el PC estaba apagado a la hora, se lanza en cuanto se pueda; con batería también.
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
	-ExecutionTimeLimit (New-TimeSpan -Hours 1) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName "homeserver - recoger copias" -Action $action -Trigger $triggers -Settings $settings `
	-Description "Trae las copias cifradas del homeserver por Tailscale (deploy/pc/recoger-copias.ps1)." -Force | Out-Null
Write-Output "Tarea creada: se ejecuta al iniciar sesión y cada 4 horas."
