<p align="center">
  <img src="images/HYDRA_UMC_BANNER.svg" alt="HYDRA-UMC-NODE-HEALING banner" width="100%">
</p>

# 💊 HYDRA-UMC-NODE-HEALING

<p align="center"><a href="README.md">🇺🇸 English</a> | <a href="README_spa.md">🇪🇸 Español</a> | <a href="README_fra.md">🇫🇷 Français</a> | <a href="README_ita.md">🇮🇹 Italiano</a> | 🇩🇪 <b>Deutsch</b> | <a href="README_zho.md">🇨🇳 简体中文</a> | <a href="README_jpn.md">🇯🇵 日本語</a></p>

### 🛡️ Hochverfügbarkeitsmonitor & Failover-Manager für HydraNodes

<p align="left">
  <img src="https://img.shields.io/badge/Lizenz-GPL%203.0-blue.svg" alt="GPL 3.0">
  <img src="https://img.shields.io/badge/Funktion-Self--Healing-green.svg" alt="Self-Healing">
  <img src="https://img.shields.io/badge/Plattform-Distributed%20Edge-blue.svg" alt="Platform">
</p>

---

## 1. 🛠️ TECHNISCHER ÜBERBLICK

**HYDRA-UMC-NODE-HEALING** ist die Resilienz-Schicht des Schwarms. Er überwacht kontinuierlich den Zustand aller physischen HydraNodes (Controller) und logischen Dienste und gewährleistet so eine Null-Ausfallzeit in der Micro-Factory.

Wenn ein Knoten aufgrund einer Hardware-Fehlfunktion oder eines Netzwerkausfalls ausfällt, löst der Healing-Manager automatisch einen Failover-Prozess aus, leitet seine aktiven Missionen an andere Knoten weiter und benachrichtigt den Bediener über den Orchestrator.

### Hauptmerkmale:
* 💓 **Health Heartbeat:** Überwachung der Knotenverfügbarkeit und des thermischen Zustands in weniger als 10 ms.
* 🔄 **Automatisches Failover:** Reassembliert Missionen von ausgefallenen Knoten transparent auf gesunde Knoten.
* 🛡️ **Soft Reboot:** Versucht eine Remote-Dienstwiederherstellung, bevor ein vollständiger Hardware-Reset ausgelöst wird.
* 📡 **Bediener-Alarme:** Echtzeit-Benachrichtigung über alle Schnittstellen (Studios, Apps, Watch).
* 🔁 **Begrenzte Retries + verifizierte Knotenidentität (v0, echt):** ein vorübergehender Netzwerkausfall kippt einen Knoten nicht mehr beim ersten Fehlschlag auf `UNREACHABLE` - `checkNode` wiederholt mit einem deterministischen, gedeckelten exponentiellen Backoff, bevor es aufgibt; ein Knoten, der antwortet, sich aber nicht korrekt selbst identifizieren kann, wird als `INVALID` klassifiziert und niemals vertraut, wiederholt oder als gesund gemeldet.

---

## 2. 🔄 HEALING-WORKFLOW

```mermaid
flowchart TB
    MONITOR["Schwarm-Zustandsmonitor"] -- Heartbeat --> N1["Knoten 1"]
    MONITOR -- Heartbeat --> N2["Knoten 2"]
    N1 -- Timeout/Fehler --> DETECT["Fehler erkannt"]
    DETECT --> DIAG["Diagnose-Engine"]
    DIAG -- Kritisch --> FAIL["FAILOVER: Jobs auf Knoten 2 verschieben"]
    DIAG -- Wiederherstellbar --> RESET["SOFT-REBOOT: Dienst neu starten"]
    FAIL --> ORCH["HYDRA-ORCHESTRATOR"]
```

---

## 3. 🧱 ARCHITEKTUR & DESIGNENTSCHEIDUNGEN

* **Warum die echte Logik unter `src/` liegt, nicht im Repo-Root.** `src/healthpb` (generierte gRPC-Stubs), `src/watchdog` (die Polling-Engine) und `src/config` (der Knotenregister-Lader) enthalten die eigentliche Implementierung; `main.go`/`version.go` bleiben im Repo-Root als der Einstiegspunkt, der sie verbindet.
* **Warum die Erkennung vom Orchestrator getrennt ist, den sie schützt.** Ein Node-Healing-Watchdog, der INNERHALB des Orchestrator-Prozesses liefe, könnte nicht erkennen, dass genau dieser Prozess hängt - als unabhängiger Dienst zu laufen ist das, was 'einen nicht reagierenden Knoten erkennen und seine Arbeit umleiten' überhaupt erst möglich macht, auch wenn der nicht reagierende Knoten der Orchestrator selbst ist.
* **Warum die Erkennung heute schon echt ist, Failover/Soft-Reboot aber nicht.** `src/watchdog` fragt bei jedem registrierten Knoten `HealthService.Check()` ab (den geteilten `hydra.common.v1`-gRPC-Vertrag aus `HYDRA-UMC-ORCHESTRATOR/proto/hydra_common.proto`), in einem echten Intervall über eine echte Netzwerkverbindung, klassifiziert das Ergebnis als HEALTHY/DEGRADED/UNHEALTHY/UNREACHABLE und löst einen `Reactor`-Callback nur bei einer tatsächlichen Zustands*änderung* aus (nie bei jedem Zyklus). Es ruft noch nicht HYDRA-UMC-ORCHESTRATOR auf, um einen echten Failover oder Soft-Reboot auszulösen, weil ORCHESTRATOR dafür ebenfalls noch keine API hat - `watchdog.Reactor` ist die Nahtstelle für später. Die Erkennung musste darauf nicht warten, um echt zu sein.
* **Warum das Knotenregister eine statische JSON-Datei ist und keine Live-Abfrage an HYDRA-UMC-SWARM-SYNC.** SWARM-SYNC (die vom README ursprünglich genannte Quelle der Wahrheit für "jede Zelle des Schwarms") hat ebenfalls noch keine echte API - es befindet sich noch im Andamiaje-Stadium. Ein statisches `nodes.json` (siehe `nodes.example.json`) ist das ehrliche v0, kein Platzhalter, der dynamisch vorgibt zu sein. `src/config.LoadNodes` gegen einen echten SWARM-SYNC-Client austauschen, sobald dieses Projekt einen hat.
* **Wie sich das ins restliche Ökosystem einfügt.** Ein Geschwisterdienst unter HYDRA-UMC-ORCHESTRATOR - überwacht jeden Knoten in seinem Register und meldet Zustandsänderungen; Arbeit von einem nicht mehr reagierenden Knoten umzuleiten ist die nächste Schicht, aufgebaut hierauf, sobald ORCHESTRATOR etwas bereitstellt, worüber Arbeit umgeleitet werden kann.
* **Warum ein Transportfehler wiederholt wird (begrenzt), eine falsche Identität aber niemals.** Eine abgelehnte Verbindung oder ein RPC-Timeout kann ein wirklich vorübergehender Ausfall sein - ein neu startender Knoten, ein kurzer Netzwerk-Hänger - daher wiederholt `checkNode` bis zu `RetryPolicy.MaxAttempts` Mal mit exponentiellem Backoff, bevor es aufgibt. Ein Knoten, der antwortet, aber den falschen Namen (oder gar keinen) meldet, ist ein völlig anderes Problem: Kein Warten repariert einen Dienst, der am falschen Port hängt - daher wird dieser Fall sofort als `StatusInvalid` klassifiziert, ganz ohne Wiederholung.
* **Warum der Backoff keinen zufälligen Jitter hat.** Eine echte Produktionsflotte würde Jitter wollen, um einen Ansturm gleichzeitiger Neuverbindungen zu vermeiden, aber dieser Watchdog fragt bereits jeden Knoten aus seiner eigenen Goroutine in seinem eigenen Tempo ab - das Einzige, was Jitter hier kosten würde, ist `RetryPolicy.Backoff()` nicht-deterministisch und in Tests schwerer zu verifizieren zu machen. Jitter hinzufügen, falls/wenn dieser Watchdog jemals Hunderte von Knoten gegen eine gemeinsame Engpassressource abfragt.

---

## 📂 VERZEICHNISSTRUKTUR

```text
HYDRA-UMC-NODE-HEALING/
├── src/
│   ├── healthpb/      # Generierte Go-Stubs für hydra.common.v1
│   │                  # (übernommen aus HYDRA-UMC-ORCHESTRATOR/proto/
│   │                  # hydra_common.proto - siehe proto/README.md
│   │                  # dieses Repos für den Codegen-Befehl)
│   ├── watchdog/      # Echte Polling-Schleife: dial, Check(), klassifizieren,
│   │                  # nur bei Zustandsänderung reagieren, plus
│   │                  # retry.go (RetryPolicy)
│   └── config/        # Lader für das statische JSON-Knotenregister
├── build/             # Kompilierte Binärdateien (Ausgabe von build.sh/.bat)
├── tools/
│   ├── build_test.py  # Nicht-versionierender Build-Check
│   └── ci_validate.py # Manifest/CHANGELOG/Docs-Validierung, von CI genutzt
├── nodes.example.json # Beispiel-Knotenregister (siehe src/config)
├── go.mod / go.sum    # Go-Modul-Definition
├── version.go         # const Version = "X.Y.Z" (go.mod hat kein solches Feld)
├── main.go            # Einstiegspunkt: lädt das Register und startet den Watchdog
├── bump_version.py    # Versions-Bump nach Kilometerzähler-Prinzip
├── build.sh/.bat      # Erhöht die Version, dann `go build`
├── build-test.sh/.bat # Nicht-versionierender Build-Check
├── run.sh/.bat        # Führt die kompilierte Binärdatei aus
├── docs/
│   ├── ARCHITECTURE.md
│   ├── BUILD_AND_RUN.md
│   └── INTEGRATION_CONTRACT.md
└── README.md
```

Aus der ursprünglichen Vorlage entfernt: `hardware/`, `firmware/`, `os/`,
`docs/`, `images/` und `scripts/` — dies ist ein reiner Softwaredienst
(Go-Binärdatei) ohne eigene Hardware oder Firmware, ohne zu pflegendes
Betriebssystem-Image, und ohne Dokumentations-/Medien-/Utility-Skript-Inhalt,
der eigene Ordner bislang rechtfertigen würde.

---

## 🔧 BUILD UND AUSFÜHRUNG

Ein echter Watchdog, nicht nur ein Skelett, das kompiliert: er wählt jeden
Knoten aus `nodes.example.json` (oder `-nodes <Pfad>` für ein eigenes
Register) per gRPC an und meldet Zustandsänderungen auf stdout.

```bash
# Windows
build.bat
run.bat -nodes nodes.example.json

# Linux / macOS
./build.sh
./run.sh -nodes nodes.example.json
```

`build.sh`/`build.bat` erhöhen die Version in `version.go` (ökosystemweite
Kilometerzähler-Regel, siehe `bump_version.py` - `go.mod` hat kein
natives Versionsfeld für Anwendungsbinärdateien) und führen anschließend
`go build` aus. `run.sh`/`run.bat` führen die resultierende Binärdatei
direkt aus.

Jeder Registereintrag braucht etwas Echtes, das auf seiner `address`
lauscht und `hydra.common.v1.HealthService` implementiert (siehe
`HYDRA-UMC-ORCHESTRATOR/proto/hydra_common.proto`), um jemals als
gesund gemeldet zu werden - da auf den Beispiel-Ports noch nichts läuft,
ist beim ersten Zyklus für alle drei `UNKNOWN -> UNREACHABLE` zu
erwarten (jetzt erst nach Erschöpfung der begrenzten Versuche von
`RetryPolicy`, nicht schon beim ersten Dial - siehe die Funktion
"Begrenzte Retries" oben), was heute das korrekte, ehrliche Ergebnis ist
(jeder Knoten des Ökosystems ist über die eigene Erkennungslogik dieses
Repos hinaus noch im Andamiaje-Stadium). Ein Knoten, der antwortet, sich
aber nicht korrekt selbst identifizieren kann, gibt stattdessen sofort
`UNKNOWN -> INVALID` aus.

```bash
go test ./...   # src/config + src/watchdog, echte gRPC-Roundtrips
                 # über echte Loopback-Sockets, ohne simulierten Client
```

---

## 🚀 FAHRPLAN
* **Phase 1:** Digital-Twin-Synchronisation mit Echtzeit-Hardware-Telemetrie und Sub-10ms-Latenz.
* **Phase 2:** Physics Replica-Integration mit industriellen Simulatoren (Isaac Sim) und Unterstützung für verformbare Körper.
* **Phase 3:** Automatisierte Wiederherstellungsmuster von Node Healing für dezentrales Failover und frühzeitige Erkennung von Sensordegradation.
* **Phase 4:** KI-gestütztes prädiktives Healing basierend auf frühzeitiger Sensordegradation und HIL-Bridge-Unterstützung für Full-Scale Vehicle-in-the-Loop.

---

## 🔗 Verwandte Projekte

Dieses Projekt ist Teil eines größeren Robotik-Ökosystems desselben Autors (JuanenRac / Electro Hobby 3D), das Firmware, Steuerungssoftware, KI-Knoten und Flotten-Tools umfasst. Gut zu wissen, denn eine Anfrage könnte tatsächlich eines dieser Projekte betreffen statt dieses Repository.

### Familie

**Elternteil:** **[HYDRA-UMC-ORCHESTRATOR](https://github.com/JuanenRac/HYDRA-UMC-ORCHESTRATOR)** — der Integrations-Elternteil, den dieser Heilungsdienst schützt.

**Geschwister:**
- **[HYDRA-UMC-SWARM-SYNC](https://github.com/JuanenRac/HYDRA-UMC-SWARM-SYNC)** — Geschwister-Orchestrierungsdienst, gleicher Elternteil.
- **[HYDRA-UMC-PATH-PLANNER-3D](https://github.com/JuanenRac/HYDRA-UMC-PATH-PLANNER-3D)** — Geschwister-Orchestrierungsdienst, gleicher Elternteil.
- **[HYDRA-UMC-JOB-DISPATCHER](https://github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER)** — Geschwister-Orchestrierungsdienst, gleicher Elternteil.

### Direkte Beziehung (außerhalb der Familie)

- **[HYDRA-UMC-SERVER](https://github.com/JuanenRac/HYDRA-UMC-SERVER)** — überwacht Instanzen dieses Backends.

### Restliches Ökosystem

**HYDRA-UMC-Plattform** — die Multi-Roboter-Mikrofabrikzelle
- **[HYDRA-UMC](https://github.com/JuanenRac/HYDRA-UMC)** — das CM5 + STM32H745-Motherboard, das bis zu 8 Roboterarme orchestriert.
- **[HYDRA-UMC-SERVER](https://github.com/JuanenRac/HYDRA-UMC-SERVER)** — das Express/WebSocket-Backend, mit dem jeder Steuerungsclient spricht.
- **[HYDRA-UMC-STUDIO](https://github.com/JuanenRac/HYDRA-UMC-STUDIO)** — webbasiertes Steuerungs-Dashboard, Multi-Roboter-3D-Visualisierung.
- **[HYDRA-UMC-ANDROID-CONTROL](https://github.com/JuanenRac/HYDRA-UMC-ANDROID-CONTROL)** — Android-Steuerungs-App über Wi-Fi/Bluetooth.
- **[HYDRA-UMC-IOS-CONTROL](https://github.com/JuanenRac/HYDRA-UMC-IOS-CONTROL)** — iOS/iPadOS-Steuerungs-App, gebaut in Flutter.
- **[HYDRA-UMC-SUITE](https://github.com/JuanenRac/HYDRA-UMC-SUITE)** — Desktop-Schwarm-Kommandozentrale (Python/PySide6).
- **[HYDRA-UMC-EDITOR-URDF](https://github.com/JuanenRac/HYDRA-UMC-EDITOR-URDF)** — Desktop-URDF-Modelleditor für den Roboterkatalog.
- **[HYDRA-UMC-DSI](https://github.com/JuanenRac/HYDRA-UMC-DSI)** — native Touch-UI für den eingebauten DSI-Touchscreen.

**URTC-Plattform** — der Werkzeugkopf-Controller, den jeder HYDRA-UMC-Roboterarm trägt
- **[URTC](https://github.com/JuanenRac/URTC)** — CAN-Bus-Werkzeugkopf-Controller, 25 Werkzeugprofile.
- **[URTC-FLASHER](https://github.com/JuanenRac/URTC-FLASHER)** — Desktop-Tool für CAN-OTA + SWD/JTAG-Flashing.
- **[URTC-TESTER](https://github.com/JuanenRac/URTC-TESTER)** — Desktop-Tool für Live-CAN-Bus-Diagnose.
- **[URTC-WEB-STUDIO](https://github.com/JuanenRac/URTC-WEB-STUDIO)** — browserbasierte Alternative über die Web-Serial-API.

**🎥 Vision-KI-Knoten (Hailo-8)**
- [HYDRA-UMC-VISION-NODE](https://github.com/JuanenRac/HYDRA-UMC-VISION-NODE)
- [HYDRA-UMC-VISION-STREAMER](https://github.com/JuanenRac/HYDRA-UMC-VISION-STREAMER)
- [HYDRA-UMC-DETECTION-HEF](https://github.com/JuanenRac/HYDRA-UMC-DETECTION-HEF)
- [HYDRA-UMC-SAFETY-ZONES](https://github.com/JuanenRac/HYDRA-UMC-SAFETY-ZONES)
- [HYDRA-UMC-VISUAL-SERVOING-API](https://github.com/JuanenRac/HYDRA-UMC-VISUAL-SERVOING-API)

**🧠 Kognitiver KI-Knoten (Hailo-10)**
- [HYDRA-UMC-COGNITIVE-NODE](https://github.com/JuanenRac/HYDRA-UMC-COGNITIVE-NODE)
- [HYDRA-UMC-VLA-ENGINE](https://github.com/JuanenRac/HYDRA-UMC-VLA-ENGINE)
- [HYDRA-UMC-VOICE-UI](https://github.com/JuanenRac/HYDRA-UMC-VOICE-UI)
- [HYDRA-UMC-SEMANTIC-PLANNER](https://github.com/JuanenRac/HYDRA-UMC-SEMANTIC-PLANNER)
- [HYDRA-UMC-DOCS-QA](https://github.com/JuanenRac/HYDRA-UMC-DOCS-QA)

**🎮 Digitaler Zwilling & Simulation**
- [HYDRA-UMC-TWIN](https://github.com/JuanenRac/HYDRA-UMC-TWIN)
- [HYDRA-UMC-PHYSICS-REPLICA](https://github.com/JuanenRac/HYDRA-UMC-PHYSICS-REPLICA)
- [HYDRA-UMC-HIL-BRIDGE](https://github.com/JuanenRac/HYDRA-UMC-HIL-BRIDGE)
- [HYDRA-UMC-SYNTHETIC-DATA-GEN](https://github.com/JuanenRac/HYDRA-UMC-SYNTHETIC-DATA-GEN)

**📊 Daten & Analytik**
- [HYDRA-UMC-DATALAKE](https://github.com/JuanenRac/HYDRA-UMC-DATALAKE)
- [HYDRA-UMC-TELEMETRY-COLLECTOR](https://github.com/JuanenRac/HYDRA-UMC-TELEMETRY-COLLECTOR)
- [HYDRA-UMC-ANOMALY-DETECTOR](https://github.com/JuanenRac/HYDRA-UMC-ANOMALY-DETECTOR)
- [HYDRA-UMC-PRODUCTION-REPORTS](https://github.com/JuanenRac/HYDRA-UMC-PRODUCTION-REPORTS)

**🏭 Industrielles Gateway**
- [HYDRA-UMC-GATEWAY-INDUSTRIAL](https://github.com/JuanenRac/HYDRA-UMC-GATEWAY-INDUSTRIAL)
- [HYDRA-UMC-OPCUA-SERVER](https://github.com/JuanenRac/HYDRA-UMC-OPCUA-SERVER)
- [HYDRA-UMC-MQTT-BROKER](https://github.com/JuanenRac/HYDRA-UMC-MQTT-BROKER)
- [HYDRA-UMC-MTCONNECT-ADAPTER](https://github.com/JuanenRac/HYDRA-UMC-MTCONNECT-ADAPTER)

**🛠️ Ergänzende Werkzeuge**
- [URTC-SMART-RACK](https://github.com/JuanenRac/URTC-SMART-RACK)
- [URTC-VISION-TOOL](https://github.com/JuanenRac/URTC-VISION-TOOL)
- [HYDRA-UMC-WATCH](https://github.com/JuanenRac/HYDRA-UMC-WATCH)
- [HYDRA-UMC-TOOL-CLI](https://github.com/JuanenRac/HYDRA-UMC-TOOL-CLI)
- [HYDRA-UMC-DASHBOARD-AI](https://github.com/JuanenRac/HYDRA-UMC-DASHBOARD-AI)


## 👤 AUTOR
**JuanenRac** (Electro Hobby 3D)
📧 electrohobby3d@gmail.com
📺 [youtube.com/@electrohobby3d](https://youtube.com/@electrohobby3d)

## 📜 LIZENZ
GPL-3.0 - Siehe LICENSE für Details.
