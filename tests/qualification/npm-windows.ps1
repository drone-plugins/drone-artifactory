$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
Set-StrictMode -Version Latest

$candidate = $env:CANDIDATE_IMAGE
if ($candidate -notmatch '@(sha256:[0-9a-f]{64})$') {
  throw 'Node candidate must be digest pinned'
}
$expectedDigest = $Matches[1]
if ([System.Environment]::OSVersion.Version.Build.ToString() -ne $env:EXPECTED_WINDOWS_BUILD) {
  throw "Expected Windows build $env:EXPECTED_WINDOWS_BUILD"
}
foreach ($command in @('pwsh', 'jf', 'jfrog', 'node', 'npm', 'npx', 'curl.exe')) {
  if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
    throw "Required command is unavailable: $command"
  }
}
if ((node --version).Trim().TrimStart('v') -ne $env:EXPECTED_NODE_VERSION) {
  throw "Unexpected Node version: $(node --version)"
}
$alternates = (& { . C:\tools\npm\Use-Npm.ps1 -List } 2>&1 | Out-String)
foreach ($version in @('5.6.0','6.4.1','6.11.3','6.14.4','6.14.7','6.14.8')) {
  if ($alternates -notmatch [regex]::Escape($version)) { throw "Missing npm $version" }
  $npmCli = "C:\tools\npm\$version\bin\npm-cli.js"
  if (-not (Test-Path -LiteralPath $npmCli -PathType Leaf)) {
    throw "npm $version CLI is missing at $npmCli"
  }
  $actualAlternate = (& node.exe $npmCli --version 2>&1 | Out-String).Trim()
  if ($actualAlternate -ne $version) {
    throw "npm $version selector returned $actualAlternate"
  }
}

$privateCaPath = 'C:\temp\qualification-private-ca.pem'
$rsa = [System.Security.Cryptography.RSA]::Create(2048)
try {
  $request = [System.Security.Cryptography.X509Certificates.CertificateRequest]::new(
    'CN=Artifactory npm qualification private CA',
    $rsa,
    [System.Security.Cryptography.HashAlgorithmName]::SHA256,
    [System.Security.Cryptography.RSASignaturePadding]::Pkcs1
  )
  $request.CertificateExtensions.Add(
    [System.Security.Cryptography.X509Certificates.X509BasicConstraintsExtension]::new($true, $false, 0, $true)
  )
  $certificate = $request.CreateSelfSigned([DateTimeOffset]::UtcNow.AddMinutes(-5), [DateTimeOffset]::UtcNow.AddDays(1))
  try {
    $privateCaThumbprint = $certificate.Thumbprint
    $privateCaPem = "-----BEGIN CERTIFICATE-----`n$([Convert]::ToBase64String(
      $certificate.Export([System.Security.Cryptography.X509Certificates.X509ContentType]::Cert),
      [Base64FormattingOptions]::InsertLineBreaks
    ))`n-----END CERTIFICATE-----`n"

    $serverRsa = [System.Security.Cryptography.RSA]::Create(2048)
    try {
      $serverRequest = [System.Security.Cryptography.X509Certificates.CertificateRequest]::new(
        'CN=localhost',
        $serverRsa,
        [System.Security.Cryptography.HashAlgorithmName]::SHA256,
        [System.Security.Cryptography.RSASignaturePadding]::Pkcs1
      )
      $san = [System.Security.Cryptography.X509Certificates.SubjectAlternativeNameBuilder]::new()
      $san.AddDnsName('localhost')
      $san.AddIpAddress([System.Net.IPAddress]::Loopback)
      $serverRequest.CertificateExtensions.Add($san.Build())
      $serverRequest.CertificateExtensions.Add(
        [System.Security.Cryptography.X509Certificates.X509BasicConstraintsExtension]::new($false, $false, 0, $true)
      )
      $serverRequest.CertificateExtensions.Add(
        [System.Security.Cryptography.X509Certificates.X509KeyUsageExtension]::new(
          [System.Security.Cryptography.X509Certificates.X509KeyUsageFlags]::DigitalSignature,
          $true
        )
      )
      $serverAuthOids = [System.Security.Cryptography.OidCollection]::new()
      [void] $serverAuthOids.Add([System.Security.Cryptography.Oid]::new('1.3.6.1.5.5.7.3.1'))
      $serverRequest.CertificateExtensions.Add(
        [System.Security.Cryptography.X509Certificates.X509EnhancedKeyUsageExtension]::new(
          $serverAuthOids,
          $true
        )
      )
      $serial = [byte[]]::new(16)
      [System.Security.Cryptography.RandomNumberGenerator]::Fill($serial)
      $serverCertificate = $serverRequest.Create(
        $certificate,
        [DateTimeOffset]::UtcNow.AddMinutes(-5),
        [DateTimeOffset]::UtcNow.AddHours(4),
        $serial
      )
      try {
        $serverCertPem = "-----BEGIN CERTIFICATE-----`n$([Convert]::ToBase64String(
          $serverCertificate.Export([System.Security.Cryptography.X509Certificates.X509ContentType]::Cert),
          [Base64FormattingOptions]::InsertLineBreaks
        ))`n-----END CERTIFICATE-----`n"
        $serverKeyPem = $serverRsa.ExportPkcs8PrivateKeyPem()
      } finally {
        $serverCertificate.Dispose()
      }
    } finally {
      $serverRsa.Dispose()
    }
  } finally {
    $certificate.Dispose()
  }
} finally {
  $rsa.Dispose()
}

$wrongCaRsa = [System.Security.Cryptography.RSA]::Create(2048)
try {
  $wrongCaRequest = [System.Security.Cryptography.X509Certificates.CertificateRequest]::new(
    'CN=Unrelated npm qualification CA',
    $wrongCaRsa,
    [System.Security.Cryptography.HashAlgorithmName]::SHA256,
    [System.Security.Cryptography.RSASignaturePadding]::Pkcs1
  )
  $wrongCaRequest.CertificateExtensions.Add(
    [System.Security.Cryptography.X509Certificates.X509BasicConstraintsExtension]::new($true, $false, 0, $true)
  )
  $wrongCaCertificate = $wrongCaRequest.CreateSelfSigned(
    [DateTimeOffset]::UtcNow.AddMinutes(-5),
    [DateTimeOffset]::UtcNow.AddDays(1)
  )
  try {
    $wrongCaPem = "-----BEGIN CERTIFICATE-----`n$([Convert]::ToBase64String(
      $wrongCaCertificate.Export([System.Security.Cryptography.X509Certificates.X509ContentType]::Cert),
      [Base64FormattingOptions]::InsertLineBreaks
    ))`n-----END CERTIFICATE-----`n"
  } finally {
    $wrongCaCertificate.Dispose()
  }
} finally {
  $wrongCaRsa.Dispose()
}

$tokenPath = 'C:\var\run\secrets\kubernetes.io\serviceaccount\token'
$caPath = 'C:\var\run\secrets\kubernetes.io\serviceaccount\ca.crt'
$token = Get-Content -LiteralPath $tokenPath -Raw
$namespace = $env:POD_NAMESPACE
$pod = if ($env:POD_NAME) { $env:POD_NAME } else { $env:COMPUTERNAME }
$base = "https://$($env:KUBERNETES_SERVICE_HOST):$($env:KUBERNETES_SERVICE_PORT_HTTPS)/api/v1/namespaces/$namespace/pods"
$curl = @('--fail','--silent','--show-error','--ssl-no-revoke','--cacert',$caPath,'-H',"Authorization: Bearer $token")
$json = & curl.exe @curl "$base/$pod"
if ($LASTEXITCODE -ne 0) {
  $listing = (& curl.exe @curl $base | ConvertFrom-Json).items
  $addresses = @(
    [System.Net.Dns]::GetHostAddresses([System.Net.Dns]::GetHostName()) |
      Where-Object AddressFamily -eq ([System.Net.Sockets.AddressFamily]::InterNetwork) |
      ForEach-Object IPAddressToString
  )
  $matches = @(
    $listing | Where-Object {
      $podIPProperty = $_.status.PSObject.Properties['podIP']
      $podIPProperty -and $addresses -contains [string] $podIPProperty.Value
    }
  )
  if ($matches.Count -ne 1) {
    throw "Could not identify the current Pod by IP in namespace $namespace"
  }
  $status = $matches[0].status
} else {
  $status = ($json | ConvertFrom-Json).status
}
$imageIds = @($status.containerStatuses | ForEach-Object { [string] $_.imageID })
if (@($imageIds | Where-Object { $_ -match "@$([regex]::Escape($expectedDigest))$" }).Count -lt 1) {
  throw "Expected running imageID $expectedDigest; observed: $($imageIds -join ', ')"
}

$artifactory = $env:JFROG_URL.TrimEnd('/')
$platform = $artifactory -replace '/artifactory$', ''
$hostName = ([uri]$platform).Host
$credentialBytes = [System.Text.Encoding]::UTF8.GetBytes("$($env:JFROG_USERNAME):$($env:JFROG_PASSWORD)")
$headers = @{Authorization = "Basic $([Convert]::ToBase64String($credentialBytes))"}

function Get-QualificationBuildInfo {
  param(
    [string]$BuildName,
    [string]$BuildNumber
  )
  $encodedName = [uri]::EscapeDataString($BuildName)
  $uri = "$artifactory/api/build/$encodedName/$BuildNumber"
  for ($attempt = 1; $attempt -le 6; $attempt++) {
    try {
      return Invoke-WebRequest -UseBasicParsing -SkipHttpErrorCheck -Headers $headers -Uri $uri
    } catch {
      if ($attempt -eq 6) { throw }
      Write-Warning "Build-info verification attempt $attempt failed: $($_.Exception.Message)"
      Start-Sleep -Seconds 5
    }
  }
}

$ping = Invoke-WebRequest -UseBasicParsing -SkipHttpErrorCheck -MaximumRedirection 0 -Headers $headers -Uri "$artifactory/api/system/ping"
$pingStatus = [int] $ping.StatusCode
if ($pingStatus -ge 300 -and $pingStatus -lt 400) {
  throw "JFrog tenant is not active; ping redirected to $($ping.Headers.Location)"
}
if ($pingStatus -lt 200 -or $pingStatus -ge 300 -or $ping.Content.Trim() -ne 'OK') {
  throw "JFrog Artifactory ping failed; HTTP $pingStatus`: $($ping.Content)"
}
$repo = "$($env:NPM_REPOSITORY_PREFIX)-ltsc$($env:LTSC)"
$repoUri = "$artifactory/api/repositories/$repo"
$probe = Invoke-WebRequest -UseBasicParsing -SkipHttpErrorCheck -Headers $headers -Uri $repoUri
$probeStatus = [int] $probe.StatusCode
if ($probeStatus -in @(400,404)) {
  $config = @{key=$repo; rclass='local'; packageType='npm'; repoLayoutRef='npm-default'} | ConvertTo-Json -Compress
  $created = Invoke-WebRequest -UseBasicParsing -SkipHttpErrorCheck -Method Put -Headers $headers -ContentType 'application/json' -Body $config -Uri $repoUri
  if ([int]$created.StatusCode -notin @(200,201)) {
    $reprobe = Invoke-WebRequest -UseBasicParsing -SkipHttpErrorCheck -Headers $headers -Uri $repoUri
    if ([int]$reprobe.StatusCode -lt 200 -or [int]$reprobe.StatusCode -ge 300) {
      throw "Could not create npm sandbox repository $repo; probe HTTP $probeStatus`: $($probe.Content); create HTTP $([int]$created.StatusCode): $($created.Content); re-probe HTTP $([int]$reprobe.StatusCode): $($reprobe.Content)"
    }
  }
} elseif ($probeStatus -lt 200 -or $probeStatus -ge 300) {
  throw "Could not inspect npm sandbox repository $repo; HTTP $probeStatus`: $($probe.Content)"
}

$project = "fixture node$($env:NODE_MAJOR) ltsc$($env:LTSC)"
New-Item -ItemType Directory -Force $project | Out-Null
$packageName = "artifactory-node$($env:NODE_MAJOR)-ltsc$($env:LTSC)-$($env:PIPELINE_SEQUENCE_ID)"
$packageVersion = "1.0.$($env:PIPELINE_SEQUENCE_ID)"
@{name=$packageName; version=$packageVersion; description='Artifactory npm qualification'; license='UNLICENSED'; files=@('sentinel.txt')} |
  ConvertTo-Json | Set-Content -LiteralPath "$project\package.json" -Encoding utf8
Push-Location $project
try {
  $npm6148 = 'C:\tools\npm\6.14.8\bin\npm-cli.js'
  $lockOutput = & node.exe $npm6148 install --package-lock-only --ignore-scripts 2>&1
  $lockExitCode = $LASTEXITCODE
  if ($lockExitCode -ne 0) {
    throw "npm 6.14.8 could not generate the qualification lockfile: $($lockOutput | Out-String)"
  }
} finally {
  Pop-Location
}

function Invoke-Publisher {
  param(
    [string]$Operation,
    [string]$URL,
    [string]$AuthMode,
    [string]$NpmVersion,
    [bool]$ExpectSuccess,
    [bool]$PublishBuildInfo = $true
  )
  $buildName = "artifactory-node$($env:NODE_MAJOR)-ltsc$($env:LTSC)-$Operation"
  $env:PLUGIN_BUILD_TOOL = 'npm'
  $env:PLUGIN_COMMAND = $Operation
  $env:PLUGIN_URL = $URL
  $env:PLUGIN_PROJECT_DIR = $project
  $env:PLUGIN_REPO_RESOLVE = $repo
  $env:PLUGIN_REPO_DEPLOY = $repo
  $env:PLUGIN_RESOLVER_ID = "npm-resolve-$($env:NODE_MAJOR)-$($env:LTSC)-$Operation-$AuthMode"
  $env:PLUGIN_DEPLOYER_ID = "npm-deploy-$($env:NODE_MAJOR)-$($env:LTSC)-$Operation-$AuthMode"
  $env:PLUGIN_BUILD_NAME = $buildName
  $env:PLUGIN_BUILD_NUMBER = $env:PIPELINE_SEQUENCE_ID
  $env:PLUGIN_MODULE = $packageName
  $env:PLUGIN_PUBLISH_BUILD_INFO = $PublishBuildInfo.ToString().ToLowerInvariant()
  $env:PLUGIN_NPM_VERSION = $NpmVersion
  $env:JFROG_CLI_HOME_DIR = "$project\.jfrog-$Operation-$AuthMode-$([guid]::NewGuid().ToString('N'))"
  $env:PLUGIN_ENABLE_PROXY = 'true'
  $env:PLUGIN_PEM_FILE_CONTENTS = $privateCaPem
  $env:PLUGIN_PEM_FILE_PATH = $privateCaPath
  $env:HARNESS_HTTPS_PROXY = 'http://127.0.0.1:9'
  $env:HARNESS_NO_PROXY = $hostName
  if ($AuthMode -eq 'token') {
    Remove-Item Env:PLUGIN_USERNAME,Env:PLUGIN_PASSWORD -ErrorAction SilentlyContinue
    $env:PLUGIN_ACCESS_TOKEN = $env:JFROG_ACCESS_TOKEN
  } else {
    Remove-Item Env:PLUGIN_ACCESS_TOKEN -ErrorAction SilentlyContinue
    $env:PLUGIN_USERNAME = $env:JFROG_USERNAME
    $env:PLUGIN_PASSWORD = if ($AuthMode -eq 'invalid') { 'known-invalid-password' } else { $env:JFROG_PASSWORD }
  }
  $rawOutput = & 'C:\bin\drone-artifactory.exe' 2>&1
  $exitCode = $LASTEXITCODE
  $output = $rawOutput | Out-String
  foreach ($secret in @($env:JFROG_PASSWORD, $env:JFROG_ACCESS_TOKEN)) {
    if ($secret -and $output -match [regex]::Escape($secret)) {
      throw 'Plugin output exposed a credential'
    }
  }
  Write-Output $output
  if ($ExpectSuccess -and $exitCode -ne 0) { throw "$Operation failed with exit code $exitCode" }
  if (-not $ExpectSuccess -and $exitCode -eq 0) { throw "$Operation unexpectedly succeeded with invalid credentials" }
  if ($ExpectSuccess) {
    $publishCount = ([regex]::Matches($output, 'rt build-publish')).Count
    $expectedPublishCount = if ($PublishBuildInfo) { 1 } else { 0 }
    if ($publishCount -ne $expectedPublishCount) {
      throw "$Operation published build info $publishCount times; expected $expectedPublishCount"
    }
    if ($PublishBuildInfo) {
      $build = Get-QualificationBuildInfo -BuildName $buildName -BuildNumber $env:PIPELINE_SEQUENCE_ID
      $buildStatus = [int] $build.StatusCode
      if ($buildStatus -lt 200 -or $buildStatus -ge 300) {
        throw "Build info is absent for $buildName; HTTP $buildStatus"
      }
    }
  } elseif ($output -notmatch '(?i)(401|403|unauthori[sz]ed|forbidden|authentication (failed|required)|incorrect (username or )?password|bad credentials|access denied)') {
    throw "Invalid-credential test failed for an unrelated reason: $output"
  }
}

Invoke-Publisher -Operation ci -URL "$platform/artifactory/" -AuthMode password -NpmVersion '6.14.8' -ExpectSuccess $true
Invoke-Publisher -Operation install -URL $platform -AuthMode password -NpmVersion '' -ExpectSuccess $true -PublishBuildInfo $false
if (-not (Test-Path -LiteralPath $privateCaPath -PathType Leaf)) {
  throw 'Injected private CA was not materialized'
}
if ((Get-Content -LiteralPath $privateCaPath -Raw).Trim() -ne $privateCaPem.Trim()) {
  throw 'Materialized private CA does not match the injected PEM'
}
$sentinelPath = Join-Path $project 'sentinel.txt'
Set-Content -LiteralPath $sentinelPath -Value "private-ca-$packageName-$packageVersion" -NoNewline -Encoding ascii
$script:sentinelSha256 = (Get-FileHash -Algorithm SHA256 $sentinelPath).Hash.ToLowerInvariant()
Invoke-Publisher -Operation publish -URL "$platform/artifactory/$repo" -AuthMode password -NpmVersion '6.14.8' -ExpectSuccess $true
$artifact = Invoke-WebRequest -UseBasicParsing -SkipHttpErrorCheck -Headers $headers -Uri "$artifactory/api/storage/$repo/$packageName/-/$packageName-$packageVersion.tgz"
if ([int]$artifact.StatusCode -ne 200) {
  throw "Published npm package is absent; HTTP $([int]$artifact.StatusCode): $($artifact.Content)"
}
Invoke-Publisher -Operation publish -URL $platform -AuthMode invalid -NpmVersion '6.14.8' -ExpectSuccess $false

$privateTlsDirectory = Join-Path $project 'private-ca-e2e'
New-Item -ItemType Directory -Force $privateTlsDirectory | Out-Null
$serverCertPath = Join-Path $privateTlsDirectory 'server-cert.pem'
$serverKeyPath = Join-Path $privateTlsDirectory 'server-key.pem'
$proxyScriptPath = Join-Path $privateTlsDirectory 'private-ca-proxy.js'
$proxyLogPath = Join-Path $privateTlsDirectory 'requests.log'
Set-Content -LiteralPath $serverCertPath -Value $serverCertPem -Encoding ascii
Set-Content -LiteralPath $serverKeyPath -Value $serverKeyPem -Encoding ascii
Set-Content -LiteralPath $proxyScriptPath -Encoding utf8 -Value @'
const fs = require('fs');
const https = require('https');
const upstream = new URL(process.env.QUALIFICATION_UPSTREAM);
const privateOrigin = 'https://127.0.0.1:8443';
const logPath = process.env.QUALIFICATION_PROXY_LOG;

function rewrite(value) {
  if (!value) return value;
  return value.split(upstream.origin).join(privateOrigin);
}

const server = https.createServer({
  key: fs.readFileSync(process.env.QUALIFICATION_SERVER_KEY),
  cert: fs.readFileSync(process.env.QUALIFICATION_SERVER_CERT),
}, (request, response) => {
  if (request.url === '/qualification-health') {
    response.writeHead(200, {'content-type': 'text/plain'});
    response.end('OK');
    return;
  }
  const headers = {...request.headers, host: upstream.host, 'accept-encoding': 'identity'};
  delete headers['content-length'];
  const proxyRequest = https.request({
    protocol: upstream.protocol,
    hostname: upstream.hostname,
    port: upstream.port || 443,
    method: request.method,
    path: request.url,
    headers,
  }, (proxyResponse) => {
    const chunks = [];
    proxyResponse.on('data', chunk => chunks.push(chunk));
    proxyResponse.on('end', () => {
      let body = Buffer.concat(chunks);
      const contentType = String(proxyResponse.headers['content-type'] || '');
      if (/json|text|javascript|xml/i.test(contentType)) {
        body = Buffer.from(rewrite(body.toString('utf8')), 'utf8');
      }
      const responseHeaders = {...proxyResponse.headers};
      delete responseHeaders['transfer-encoding'];
      delete responseHeaders['content-length'];
      if (responseHeaders.location) responseHeaders.location = rewrite(responseHeaders.location);
      responseHeaders['content-length'] = String(body.length);
      response.writeHead(proxyResponse.statusCode, responseHeaders);
      response.end(body);
      fs.appendFileSync(logPath, `${request.method} ${request.url} ${proxyResponse.statusCode}\n`);
    });
  });
  proxyRequest.on('error', error => {
    fs.appendFileSync(logPath, `${request.method} ${request.url} ERROR ${error.message}\n`);
    response.writeHead(502, {'content-type': 'text/plain'});
    response.end('proxy error');
  });
  request.pipe(proxyRequest);
});
server.listen(8443, '127.0.0.1');
'@

$env:QUALIFICATION_UPSTREAM = $platform
$env:QUALIFICATION_PROXY_LOG = $proxyLogPath
$env:QUALIFICATION_SERVER_KEY = $serverKeyPath
$env:QUALIFICATION_SERVER_CERT = $serverCertPath
$proxyStdout = Join-Path $privateTlsDirectory 'proxy.stdout.log'
$proxyStderr = Join-Path $privateTlsDirectory 'proxy.stderr.log'
$quotedProxyScriptPath = '"' + $proxyScriptPath + '"'
$proxyProcess = Start-Process -FilePath node.exe -ArgumentList $quotedProxyScriptPath -PassThru `
  -RedirectStandardOutput $proxyStdout -RedirectStandardError $proxyStderr
try {
  $ready = $false
  for ($attempt = 1; $attempt -le 30; $attempt++) {
    try {
      $client = [System.Net.Sockets.TcpClient]::new('127.0.0.1', 8443)
      $client.Dispose()
      $ready = $true
      break
    } catch {
      Start-Sleep -Seconds 1
    }
  }
  if (-not $ready) {
    throw "Private-CA proxy did not start: $(Get-Content -LiteralPath $proxyStderr -Raw -ErrorAction SilentlyContinue)"
  }

  $consumerProject = Join-Path (Get-Location).Path "consumer node$($env:NODE_MAJOR) ltsc$($env:LTSC)"
  New-Item -ItemType Directory -Force $consumerProject | Out-Null
  @{
    name = "consumer-node$($env:NODE_MAJOR)-ltsc$($env:LTSC)-$($env:PIPELINE_SEQUENCE_ID)"
    version = '1.0.0'
    private = $true
    dependencies = @{$packageName = $packageVersion}
  } | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath "$consumerProject\package.json" -Encoding utf8

  function Invoke-PrivateCAInstall {
    param(
      [string]$Case,
      [AllowEmptyString()][string]$PemContents,
      [bool]$ExpectSuccess
    )
    Remove-Item -Recurse -Force "$consumerProject\node_modules","$consumerProject\package-lock.json" -ErrorAction SilentlyContinue
    $env:PLUGIN_BUILD_TOOL = 'npm'
    $env:PLUGIN_COMMAND = 'install'
    $env:PLUGIN_URL = 'https://127.0.0.1:8443/artifactory'
    $env:PLUGIN_PROJECT_DIR = $consumerProject
    $env:PLUGIN_REPO_RESOLVE = $repo
    $env:PLUGIN_REPO_DEPLOY = $repo
    $env:PLUGIN_RESOLVER_ID = "private-ca-$Case-$($env:NODE_MAJOR)-$($env:LTSC)"
    $env:PLUGIN_DEPLOYER_ID = "private-ca-deploy-$Case-$($env:NODE_MAJOR)-$($env:LTSC)"
    $env:PLUGIN_BUILD_NAME = "private-ca-$Case-node$($env:NODE_MAJOR)-ltsc$($env:LTSC)"
    $env:PLUGIN_BUILD_NUMBER = $env:PIPELINE_SEQUENCE_ID
    $env:PLUGIN_MODULE = "private-ca-$Case"
    $env:PLUGIN_PUBLISH_BUILD_INFO = 'true'
    $env:PLUGIN_NPM_VERSION = '6.14.8'
    $env:PLUGIN_USERNAME = $env:JFROG_USERNAME
    $env:PLUGIN_PASSWORD = $env:JFROG_PASSWORD
    $env:PLUGIN_ENABLE_PROXY = 'false'
    $env:JFROG_CLI_HOME_DIR = "$consumerProject\.jfrog-$Case-$([guid]::NewGuid().ToString('N'))"
    $env:NPM_CONFIG_CACHE = "$consumerProject\.npm-cache-$Case-$([guid]::NewGuid().ToString('N'))"
    $env:NO_PROXY = 'localhost,127.0.0.1'
    Remove-Item Env:PLUGIN_ACCESS_TOKEN,Env:HARNESS_HTTPS_PROXY,Env:HARNESS_HTTP_PROXY -ErrorAction SilentlyContinue
    if ($PemContents) {
      $env:PLUGIN_PEM_FILE_CONTENTS = $PemContents
      $env:PLUGIN_PEM_FILE_PATH = "$consumerProject\$Case-ca.pem"
    } else {
      Remove-Item Env:PLUGIN_PEM_FILE_CONTENTS,Env:PLUGIN_PEM_FILE_PATH -ErrorAction SilentlyContinue
    }

    $rawOutput = & 'C:\bin\drone-artifactory.exe' 2>&1
    $exitCode = $LASTEXITCODE
    $output = $rawOutput | Out-String
    if ($output -match [regex]::Escape($env:JFROG_PASSWORD)) {
      throw 'Private-CA test output exposed a credential'
    }
    Write-Output $output
    if (-not $ExpectSuccess) {
      if ($exitCode -eq 0) { throw "$Case unexpectedly trusted the private-CA endpoint" }
      if ($output -notmatch '(?i)x509: certificate signed by unknown authority') {
        throw "$Case failed without the expected private-CA diagnostic: $output"
      }
      if (Test-Path -LiteralPath "$consumerProject\node_modules") {
        throw "$Case populated node_modules despite the TLS failure"
      }
      return
    }
    if ($exitCode -ne 0) { throw "$Case failed with exit code $exitCode`: $output" }
    if (([regex]::Matches($output, 'rt build-publish')).Count -ne 1) {
      throw "$Case did not publish build info exactly once"
    }
    $sentinel = "$consumerProject\node_modules\$packageName\sentinel.txt"
    if (-not (Test-Path -LiteralPath $sentinel -PathType Leaf)) {
      throw "$Case did not install the private package sentinel"
    }
    if ((Get-FileHash -Algorithm SHA256 $sentinel).Hash.ToLowerInvariant() -ne $script:sentinelSha256) {
      throw "$Case installed an unexpected package payload"
    }
    $build = Get-QualificationBuildInfo -BuildName $env:PLUGIN_BUILD_NAME -BuildNumber $env:PIPELINE_SEQUENCE_ID
    if ([int]$build.StatusCode -ne 200) {
      throw "$Case build info is absent; HTTP $([int]$build.StatusCode)"
    }
  }

  Invoke-PrivateCAInstall -Case 'no-ca' -PemContents '' -ExpectSuccess $false
  Invoke-PrivateCAInstall -Case 'wrong-ca' -PemContents $wrongCaPem -ExpectSuccess $false
  Invoke-PrivateCAInstall -Case 'correct-ca' -PemContents $privateCaPem -ExpectSuccess $true

  foreach ($store in @('CurrentUser','LocalMachine')) {
    if (Test-Path -LiteralPath "Cert:\$store\Root\$privateCaThumbprint") {
      throw "The temporary npm private CA remained in the Windows $store ROOT store"
    }
  }
  $proxyLog = Get-Content -LiteralPath $proxyLogPath -Raw
  foreach ($expectedRequest in @('/api/system/version','/api/npm/auth',$packageName,'.tgz','/api/build')) {
    if ($proxyLog -notmatch [regex]::Escape($expectedRequest)) {
      throw "Private-CA proxy did not observe $expectedRequest`: $proxyLog"
    }
  }
} finally {
  if ($proxyProcess -and -not $proxyProcess.HasExited) {
    Stop-Process -Id $proxyProcess.Id -Force
  }
}

Write-Output "Node $($env:EXPECTED_NODE_VERSION) LTSC $($env:LTSC) npm qualification passed for $candidate"
