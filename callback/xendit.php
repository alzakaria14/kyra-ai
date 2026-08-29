<?php
declare(strict_types=1);

// Endpoint publik untuk Xendit. File ini tidak menjalankan aplikasi Go;
// ia hanya memverifikasi callback lalu meneruskannya ke Kyra di localhost.
if ($_SERVER['REQUEST_METHOD'] !== 'POST') {
    http_response_code(405);
    header('Allow: POST');
    exit;
}

function loadEnvFile(string $path): array
{
    if (!is_readable($path)) {
        return [];
    }

    $values = [];
    foreach (file($path, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) as $line) {
        $line = trim($line);
        if ($line === '' || str_starts_with($line, '#') || !str_contains($line, '=')) {
            continue;
        }
        [$key, $value] = explode('=', $line, 2);
        $values[trim($key)] = trim(trim($value), "\\\"'");
    }
    return $values;
}

$config = loadEnvFile(dirname(__DIR__) . DIRECTORY_SEPARATOR . '.env');
$body = file_get_contents('php://input');
if ($body === false || trim($body) === '' || json_decode($body, true) === null) {
    http_response_code(400);
    echo 'invalid JSON';
    exit;
}

$callbackToken = $_SERVER['HTTP_X_CALLBACK_TOKEN'] ?? '';
$expectedToken = $config['XENDIT_CALLBACK_TOKEN'] ?? '';
if ($expectedToken !== '' && !hash_equals($expectedToken, $callbackToken)) {
    http_response_code(401);
    echo 'unauthorized';
    exit;
}

$port = preg_replace('/[^0-9]/', '', $config['PORT'] ?? '8080') ?: '8080';
$target = 'http://127.0.0.1:' . $port . '/webhook/xendit';
$headers = "Content-Type: application/json\r\nAccept: application/json\r\n";
if ($callbackToken !== '') {
    $headers .= 'X-Callback-Token: ' . $callbackToken . "\r\n";
}

$context = stream_context_create(['http' => [
    'method' => 'POST',
    'header' => $headers,
    'content' => $body,
    'timeout' => 15,
    'ignore_errors' => true,
]]);

$response = @file_get_contents($target, false, $context);
$statusLine = $http_response_header[0] ?? '';
preg_match('/\s(\d{3})\s/', $statusLine, $matches);
$status = isset($matches[1]) ? (int) $matches[1] : 502;

if ($status >= 200 && $status < 300) {
    http_response_code(204);
    exit;
}

error_log('Kyra Xendit relay failed with HTTP ' . $status);
http_response_code(502);
echo 'callback relay failed';
