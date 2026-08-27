@echo off
REM =============================================================================
REM HYDRA-UMC-NODE-HEALING - run.bat
REM Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
REM GPL-3.0 - see LICENSE
REM =============================================================================
REM HYDRA-UMC-NODE-HEALING - run.bat
REM Runs the already-built binary. Run build.bat first.
setlocal
cd /d "%~dp0"

if exist build\hydra-umc-node-healing.exe (
    build\hydra-umc-node-healing.exe %*
) else (
    echo No compiled binary found. Run build.bat first.
    pause
    exit /b 1
)
endlocal
pause
