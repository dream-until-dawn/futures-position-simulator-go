$ErrorActionPreference='Stop'
# 只准备CI的WMI/CIM服务；后续生产身份查询仍使用原10秒预算。
$rows=@(Get-CimInstance -ClassName Win32_Process)
if($rows.Count -eq 0){throw 'CIM fixture returned no processes'}
Write-Output 'CIM fixture ready; production budgets unchanged'
