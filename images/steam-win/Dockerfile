# Kraken steam-win — Windows-native game-server base image.
#
# Windows Server Core + SteamCMD (steamcmd.exe). Game Specs whose platform is
# "windows-native" use this image on a Windows node (Docker in Windows-container
# mode). The install phase runs `steamcmd.exe +app_update <appid>` into the
# server's data volume (mounted at C:\data); the runtime phase runs the server
# binary. The Agent overrides Entrypoint/Cmd per server with the spec's install
# script or startup command (run via `cmd /S /C`).
#
# Note: Windows base images are large and host-version sensitive. ltsc2022 runs
# on Windows 10/11 and Server 2022 via Hyper-V isolation. Build on a Windows host
# with Docker in Windows-container mode:
#   docker build -t ghcr.io/briggleman/kraken-steam-win:ltsc2022 images/steam-win

# Stage 1: source the desktop/runtime DLLs that Server Core omits but Unity/UE
# game servers (e.g. V Rising) statically import. They come from the larger
# Windows Server base; XInput 1.3 isn't in any base, so it's pulled from the
# legacy DirectX redistributable. (Determined empirically by diffing V Rising's
# loaded modules against Server Core — see SECURITY/BACKLOG notes.)
FROM mcr.microsoft.com/windows/server:ltsc2022 AS deps
SHELL ["powershell", "-NoLogo", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop';"]
RUN New-Item -ItemType Directory -Force C:\sys32 | Out-Null; \
    foreach ($d in 'directxdatabasehelper.dll','glu32.dll','iconcodecservice.dll','msacm32.dll','opengl32.dll','rasadhlp.dll') { Copy-Item ('C:\Windows\System32\' + $d) C:\sys32\ }; \
    Invoke-WebRequest -UseBasicParsing -Uri 'https://download.microsoft.com/download/8/4/A/84A35BF1-DAFE-4AE8-82AF-AD2AE20B6B14/directx_Jun2010_redist.exe' -OutFile C:\dxr.exe; \
    Start-Process -Wait -FilePath C:\dxr.exe -ArgumentList '/Q','/T:C:\dxr'; \
    expand C:\dxr\APR2007_xinput_x64.cab -F:xinput1_3.dll C:\sys32 | Out-Null

# Stage 2: the runtime image — Server Core + SteamCMD + the copied DLLs.
FROM mcr.microsoft.com/windows/servercore:ltsc2022

SHELL ["powershell", "-NoLogo", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop';"]

# Install SteamCMD into C:\steamcmd and add it to PATH.
RUN New-Item -ItemType Directory -Force C:\steamcmd | Out-Null; \
    Invoke-WebRequest -UseBasicParsing -Uri 'https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip' -OutFile C:\steamcmd\steamcmd.zip; \
    Expand-Archive -Path C:\steamcmd\steamcmd.zip -DestinationPath C:\steamcmd -Force; \
    Remove-Item C:\steamcmd\steamcmd.zip; \
    $p = [Environment]::GetEnvironmentVariable('Path','Machine') + ';C:\steamcmd'; \
    [Environment]::SetEnvironmentVariable('Path', $p, 'Machine')

# Prime SteamCMD so its first-run self-update is baked into the image layer.
RUN Start-Process -Wait -NoNewWindow -FilePath C:\steamcmd\steamcmd.exe -ArgumentList '+quit'

# Install the Visual C++ runtime. Server Core ships without it, so Unity/UE game
# servers (e.g. V Rising) fail to start with 0xC0000135 (vcruntime140.dll /
# msvcp140.dll not found). The redistributable provides those DLLs.
RUN Invoke-WebRequest -UseBasicParsing -Uri 'https://aka.ms/vs/17/release/vc_redist.x64.exe' -OutFile C:\vc_redist.x64.exe; \
    $proc = Start-Process -Wait -PassThru -FilePath C:\vc_redist.x64.exe -ArgumentList '/quiet','/norestart'; \
    if ($proc.ExitCode -ne 0 -and $proc.ExitCode -ne 3010) { throw \"vc_redist install failed: $($proc.ExitCode)\" }; \
    Remove-Item C:\vc_redist.x64.exe

# Copy the desktop/runtime DLLs Unity/UE servers need (sourced in the deps stage).
COPY --from=deps C:/sys32/ C:/Windows/System32/

# Server data lives on a mounted volume here. Use forward slashes — this Docker
# daemon's Windows volume parser rejects the backslash JSON form ("C:\\data").
VOLUME ["C:/data"]
WORKDIR C:/data

# Default entrypoint is cmd; the Agent overrides Entrypoint/Cmd per server.
ENTRYPOINT ["cmd", "/S", "/C"]
CMD ["steamcmd.exe +quit"]
