# Run from GoLand terminal, from the practice7 directory: .\demo.ps1
# Educational demo of six HTTP services and asynchronous business events.
$ErrorActionPreference = 'Stop'

function Send-Json($method, $url, $body) {
    $json = $body | ConvertTo-Json -Depth 10 -Compress
    Invoke-RestMethod -Method $method -Uri $url -ContentType 'application/json; charset=utf-8' -Body ([System.Text.Encoding]::UTF8.GetBytes($json))
}
function Show-Result($label, $value) {
    Write-Host "`n========== $label ==========" -ForegroundColor Cyan
    $value | ConvertTo-Json -Depth 10
}
Write-Host "Checking all 6 services..." -ForegroundColor Yellow
$ports = @(8081,8082,8083,8084,8085,8086)
foreach ($port in $ports) {
    $response = Invoke-RestMethod -Uri "http://localhost:$port/health"
    Write-Host "$port $($response.service) : $($response.status)" -ForegroundColor Green
}

# 1. Accounts
$email = "student_$(Get-Random)@example.test"
$user = Send-Json 'POST' 'http://localhost:8081/users' @{email=$email;password='demo1234';name='Student'}
$login = Send-Json 'POST' 'http://localhost:8081/auth/login' @{email=$email;password='demo1234'}
Show-Result '1. User registered (accounts)' $user
Write-Host 'Login returned a Bearer token (hidden from output).'

# 2. Catalog from PostgreSQL
$product = Invoke-RestMethod 'http://localhost:8082/products/p-7'
Show-Result '2. Product from PostgreSQL (catalog)' $product

# 3. Cart -> HTTP call to Catalog
$cart = Send-Json 'POST' "http://localhost:8083/carts/$($user.userId)/items" @{productId='p-7';quantity=2}
Show-Result '3. Cart -> Catalog over HTTP (carts)' $cart

# 4. Order -> HTTP calls to Cart, Delivery, Payments; Delivery -> Order
$order = Send-Json 'POST' 'http://localhost:8086/orders' @{userId=$user.userId; address=@{city='Moscow';street='Tverskaya';house='1'};deliveryMethodId='courier'}
Show-Result '4. New order; awaiting_payment (orders)' $order

# 5. Payments -> RabbitMQ -> Orders
$confirmed = Send-Json 'POST' "http://localhost:8085/payments/$($order.paymentId)/confirm" @{providerTransactionId='demo-transaction-001'}
Show-Result '5. Payment confirmed (payments)' $confirmed
$paid = $null
for ($i=0; $i -lt 15; $i++) {
    $paid = Invoke-RestMethod "http://localhost:8086/orders/$($order.orderId)"
    if ($paid.status -eq 'paid') {break}
    Start-Sleep -Seconds 1
}
Show-Result '6. Order updated asynchronously via RabbitMQ (orders)' $paid
if ($paid.status -ne 'paid') {throw "Expected paid status, got $($paid.status)"}

# 6. Orders -> RabbitMQ -> Payments (refund event)
$cancelled = Send-Json 'POST' "http://localhost:8086/orders/$($order.orderId)/cancel" @{reason='Customer request'}
Show-Result '7. Order cancelled (orders)' $cancelled
$refunded = $null
for ($i=0; $i -lt 15; $i++) {
    $refunded = Invoke-RestMethod "http://localhost:8085/payments/$($order.paymentId)"
    if ($refunded.status -eq 'refunded') {break}
    Start-Sleep -Seconds 1
}
Show-Result '8. Payment refunded asynchronously (payments)' $refunded
if ($refunded.status -ne 'refunded') {throw "Expected refunded status, got $($refunded.status)"}

Write-Host "`nSUCCESS: all six services, PostgreSQL, HTTP calls and RabbitMQ events work." -ForegroundColor Green
Write-Host 'RabbitMQ UI: http://localhost:15673 (miar / miar_pass)'
