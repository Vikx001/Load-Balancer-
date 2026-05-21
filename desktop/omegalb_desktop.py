import asyncio
import importlib
import json
import multiprocessing as mp
import os
import secrets
import signal
import socket
import subprocess
import sys
import time
import urllib.request
import webbrowser
from pathlib import Path

from PySide6.QtCore import QTimer, Qt
from PySide6.QtGui import QAction
from PySide6.QtWidgets import (
    QApplication,
    QGridLayout,
    QGroupBox,
    QHBoxLayout,
    QLabel,
    QMainWindow,
    QMessageBox,
    QPushButton,
    QCheckBox,
    QTableWidget,
    QTableWidgetItem,
    QTextEdit,
    QVBoxLayout,
    QWidget,
)

try:
    import yaml
except Exception:
    yaml = None


def _bundled_root() -> Path:
    if getattr(sys, "frozen", False):
        return Path(getattr(sys, "_MEIPASS", Path(sys.executable).parent))
    return Path(__file__).resolve().parents[1]


def _runtime_root() -> Path:
    if getattr(sys, "frozen", False):
        return Path(sys.executable).resolve().parent
    return Path(__file__).resolve().parents[1]


def _run_module_main(module_name: str, env_overrides: dict[str, str] | None = None):
    runtime = _runtime_root()
    bundled = _bundled_root()
    os.chdir(runtime)
    if str(runtime) not in sys.path:
        sys.path.insert(0, str(runtime))
    if str(bundled) not in sys.path:
        sys.path.insert(0, str(bundled))
    if env_overrides:
        os.environ.update(env_overrides)
    mod = importlib.import_module(module_name)
    if not hasattr(mod, "main"):
        raise RuntimeError(f"Module {module_name} does not expose main()")
    asyncio.run(mod.main())


class OmegaDesktop(QMainWindow):
    def __init__(self):
        super().__init__()
        self.setWindowTitle("Omega-LB Desktop")
        self.resize(1280, 800)

        self.processes: dict[str, mp.Process] = {}
        self.last_snapshot: dict | None = None
        self.runtime_started = False

        self.proxy_url = "http://127.0.0.1:8080"
        self.status_url = f"{self.proxy_url}/_omega/status"
        self.config_path = self._resolve_config_path()
        self.admin_token = secrets.token_urlsafe(24)
        self.admin_allowlist = "127.0.0.1/32,::1/128"
        self.admin_rate_limit = "30"

        self._build_ui()
        self._load_wiring_from_config()
        self._setup_timer()

    def _resolve_config_path(self) -> str:
        runtime = _runtime_root()
        bundled = _bundled_root()
        runtime_cfg = runtime / "omega-lb.yaml"
        bundled_cfg = bundled / "omega-lb.yaml"
        if runtime_cfg.exists():
            return str(runtime_cfg)
        return str(bundled_cfg)

    def _build_ui(self):
        central = QWidget()
        root = QVBoxLayout(central)
        root.setContentsMargins(18, 18, 18, 18)
        root.setSpacing(14)

        header = QHBoxLayout()
        title = QLabel("Omega-LB Desktop")
        title.setObjectName("Title")
        subtitle = QLabel("Start, monitor, and validate your load balancer with one click")
        subtitle.setObjectName("Subtitle")

        title_col = QVBoxLayout()
        title_col.addWidget(title)
        title_col.addWidget(subtitle)

        self.status_chip = QLabel("OFFLINE")
        self.status_chip.setObjectName("ChipOffline")
        self.status_chip.setAlignment(Qt.AlignCenter)
        self.status_chip.setFixedWidth(140)

        header.addLayout(title_col)
        header.addStretch(1)
        header.addWidget(self.status_chip)

        root.addLayout(header)

        controls = QHBoxLayout()
        self.btn_start = QPushButton("Start Stack")
        self.btn_start.clicked.connect(self.start_stack)
        self.btn_stop = QPushButton("Stop Stack")
        self.btn_stop.clicked.connect(self.stop_stack)
        self.btn_open_dashboard = QPushButton("Open Dashboard")
        self.btn_open_dashboard.clicked.connect(self.open_dashboard)
        self.btn_open_status = QPushButton("Open Status API")
        self.btn_open_status.clicked.connect(self.open_status)
        self.btn_refresh = QPushButton("Refresh Now")
        self.btn_refresh.clicked.connect(self.poll_status)
        self.chk_managed_backends = QCheckBox("Start local managed backends")
        self.chk_managed_backends.setChecked(True)
        self.chk_loadgen = QCheckBox("Auto-start load generator")
        self.chk_loadgen.setChecked(True)
        self.chk_dashboard = QCheckBox("Auto-start dashboard")
        self.chk_dashboard.setChecked(True)

        controls.addWidget(self.btn_start)
        controls.addWidget(self.btn_stop)
        controls.addWidget(self.btn_open_dashboard)
        controls.addWidget(self.btn_open_status)
        controls.addWidget(self.btn_refresh)
        controls.addWidget(self.chk_managed_backends)
        controls.addWidget(self.chk_loadgen)
        controls.addWidget(self.chk_dashboard)
        controls.addStretch(1)
        root.addLayout(controls)

        kpi_grid = QGridLayout()
        self.kpi_tick = self._make_kpi("Tick", "-")
        self.kpi_rps = self._make_kpi("RPS", "-")
        self.kpi_total = self._make_kpi("Total Requests", "-")
        self.kpi_err = self._make_kpi("Error Rate", "-")
        self.kpi_health = self._make_kpi("Healthy Backends", "-")

        kpi_grid.addWidget(self.kpi_tick["box"], 0, 0)
        kpi_grid.addWidget(self.kpi_rps["box"], 0, 1)
        kpi_grid.addWidget(self.kpi_total["box"], 0, 2)
        kpi_grid.addWidget(self.kpi_err["box"], 0, 3)
        kpi_grid.addWidget(self.kpi_health["box"], 0, 4)
        root.addLayout(kpi_grid)

        main = QHBoxLayout()

        self.backends_table = QTableWidget(0, 9)
        self.backends_table.setHorizontalHeaderLabels(
            ["Backend", "Health", "Latency ms", "Load", "Err %", "Total", "vNodes", "Rate", "KAN wt"]
        )
        self.backends_table.verticalHeader().setVisible(False)
        self.backends_table.setAlternatingRowColors(True)
        self.backends_table.horizontalHeader().setStretchLastSection(True)
        main.addWidget(self.backends_table, 3)

        right_col = QVBoxLayout()

        logs_box = QGroupBox("Activity")
        logs_layout = QVBoxLayout(logs_box)
        self.logs = QTextEdit()
        self.logs.setReadOnly(True)
        logs_layout.addWidget(self.logs)

        right_col.addWidget(logs_box, 3)

        info_box = QGroupBox("Runtime")
        info_layout = QVBoxLayout(info_box)
        self.runtime_label = QLabel(f"Config: {self.config_path}")
        self.runtime_label.setWordWrap(True)
        self.runtime_mode = QLabel("Security mode: local-only admin control path")
        self.runtime_mode.setWordWrap(True)
        info_layout.addWidget(self.runtime_label)
        info_layout.addWidget(self.runtime_mode)
        right_col.addWidget(info_box, 1)

        wiring_box = QGroupBox("Backend Wiring")
        wiring_layout = QVBoxLayout(wiring_box)

        wiring_help = QLabel("Edit real backend targets and save. Proxy will route to these hosts/ports.")
        wiring_help.setWordWrap(True)
        wiring_layout.addWidget(wiring_help)

        self.wiring_table = QTableWidget(0, 4)
        self.wiring_table.setHorizontalHeaderLabels(["Name", "Host", "Port", "Zone"])
        self.wiring_table.verticalHeader().setVisible(False)
        self.wiring_table.horizontalHeader().setStretchLastSection(True)
        self.wiring_table.setAlternatingRowColors(True)
        wiring_layout.addWidget(self.wiring_table)

        wiring_actions = QHBoxLayout()
        self.btn_add_backend = QPushButton("Add Backend")
        self.btn_add_backend.clicked.connect(self._add_backend_row)
        self.btn_remove_backend = QPushButton("Remove Selected")
        self.btn_remove_backend.clicked.connect(self._remove_selected_backend_rows)
        self.btn_demo_wiring = QPushButton("Load Demo Targets")
        self.btn_demo_wiring.clicked.connect(self._load_demo_wiring)
        self.btn_save_wiring = QPushButton("Save Wiring")
        self.btn_save_wiring.clicked.connect(self._save_wiring_to_config)

        wiring_actions.addWidget(self.btn_add_backend)
        wiring_actions.addWidget(self.btn_remove_backend)
        wiring_actions.addWidget(self.btn_demo_wiring)
        wiring_actions.addWidget(self.btn_save_wiring)
        wiring_actions.addStretch(1)
        wiring_layout.addLayout(wiring_actions)

        right_col.addWidget(wiring_box, 2)

        main.addLayout(right_col, 2)
        root.addLayout(main)

        self.setCentralWidget(central)

        open_proxy = QAction("Open Proxy Status", self)
        open_proxy.triggered.connect(self.open_status)
        open_dashboard = QAction("Open Dashboard", self)
        open_dashboard.triggered.connect(self.open_dashboard)
        self.menuBar().addAction(open_proxy)
        self.menuBar().addAction(open_dashboard)

        self.setStyleSheet(
            """
            QMainWindow { background: #0b1220; }
            QLabel { color: #dbe7ff; font-size: 13px; }
            #Title { font-size: 30px; font-weight: 700; color: #f6fbff; }
            #Subtitle { color: #9fb3d7; font-size: 14px; }
            #ChipOffline { background: #402030; color: #ff9cb0; border: 1px solid #7a304e; border-radius: 16px; padding: 6px; font-weight: 700; }
            #ChipOnline { background: #15392b; color: #87f7c0; border: 1px solid #1f6a4b; border-radius: 16px; padding: 6px; font-weight: 700; }
            QPushButton { background: #193051; color: #e6f0ff; border: 1px solid #30517c; border-radius: 8px; padding: 10px 16px; font-weight: 600; }
            QPushButton:hover { background: #21416c; }
            QGroupBox { color: #dbe7ff; border: 1px solid #1f3458; border-radius: 10px; margin-top: 8px; padding: 8px; }
            QGroupBox::title { subcontrol-origin: margin; left: 8px; padding: 0 4px; }
            QTableWidget, QTextEdit { background: #101b2f; color: #dbe7ff; border: 1px solid #233a5f; border-radius: 10px; }
            QHeaderView::section { background: #152744; color: #c8daf8; padding: 6px; border: none; font-weight: 700; }
            """
        )

    def _run_dashboard(self):
        runtime = _runtime_root()
        bundled = _bundled_root()
        os.chdir(runtime)
        if str(runtime) not in sys.path:
            sys.path.insert(0, str(runtime))
        if str(bundled) not in sys.path:
            sys.path.insert(0, str(bundled))
        cmd = [
            sys.executable,
            "-m",
            "streamlit",
            "run",
            "dashboard/app.py",
            "--server.port=8501",
            "--server.address=127.0.0.1",
            "--server.headless=true",
            "--browser.gatherUsageStats=false",
        ]
        subprocess.run(cmd, check=False)

    def _read_config(self) -> dict:
        if yaml is None:
            raise RuntimeError("PyYAML is required for config editing but is not available.")
        path = Path(self.config_path)
        if not path.exists():
            return {}
        with open(path, "r", encoding="utf-8") as f:
            return yaml.safe_load(f) or {}

    def _write_config(self, cfg: dict):
        if yaml is None:
            raise RuntimeError("PyYAML is required for config editing but is not available.")
        path = Path(self.config_path)
        with open(path, "w", encoding="utf-8") as f:
            yaml.safe_dump(cfg, f, sort_keys=False)

    def _add_backend_row(self, name="backend-new", host="127.0.0.1", port="9000", zone="local"):
        r = self.wiring_table.rowCount()
        self.wiring_table.insertRow(r)
        self.wiring_table.setItem(r, 0, QTableWidgetItem(str(name)))
        self.wiring_table.setItem(r, 1, QTableWidgetItem(str(host)))
        self.wiring_table.setItem(r, 2, QTableWidgetItem(str(port)))
        self.wiring_table.setItem(r, 3, QTableWidgetItem(str(zone)))

    def _remove_selected_backend_rows(self):
        rows = sorted({idx.row() for idx in self.wiring_table.selectedIndexes()}, reverse=True)
        for r in rows:
            self.wiring_table.removeRow(r)

    def _load_demo_wiring(self):
        self.wiring_table.setRowCount(0)
        for i in range(4):
            self._add_backend_row(
                name=f"backend-{i}",
                host="127.0.0.1",
                port=str(9000 + i),
                zone=f"local-{chr(ord('a') + (i % 3))}",
            )
        self._log("Loaded demo backend targets into wiring table.")

    def _extract_backends_from_ui(self) -> list[dict]:
        backends = []
        for r in range(self.wiring_table.rowCount()):
            name_item = self.wiring_table.item(r, 0)
            host_item = self.wiring_table.item(r, 1)
            port_item = self.wiring_table.item(r, 2)
            zone_item = self.wiring_table.item(r, 3)

            name = (name_item.text().strip() if name_item else f"backend-{r}")
            host = (host_item.text().strip() if host_item else "")
            port_txt = (port_item.text().strip() if port_item else "")
            zone = (zone_item.text().strip() if zone_item else "local")

            if not host:
                raise ValueError(f"Backend row {r+1}: host is required")
            if not port_txt.isdigit():
                raise ValueError(f"Backend row {r+1}: port must be a number")

            port = int(port_txt)
            if not (1 <= port <= 65535):
                raise ValueError(f"Backend row {r+1}: port must be in [1, 65535]")

            backends.append({"name": name or f"backend-{r}", "host": host, "port": port, "zone": zone or "local"})

        if not backends:
            raise ValueError("At least one backend is required")
        return backends

    def _load_wiring_from_config(self):
        try:
            cfg = self._read_config()
            proxy = cfg.get("proxy", {})
            host = proxy.get("host", "127.0.0.1")
            port = int(proxy.get("port", 8080))
            self.proxy_url = f"http://{host}:{port}"
            self.status_url = f"{self.proxy_url}/_omega/status"

            self.wiring_table.setRowCount(0)
            for i, b in enumerate(cfg.get("backends", [])):
                self._add_backend_row(
                    name=b.get("name", f"backend-{i}"),
                    host=b.get("host", "127.0.0.1"),
                    port=str(b.get("port", 9000 + i)),
                    zone=b.get("zone", "local"),
                )
            if self.wiring_table.rowCount() == 0:
                self._load_demo_wiring()
        except Exception as e:
            self._log(f"Could not load backend wiring from config: {e}")
            self._load_demo_wiring()

    def _save_wiring_to_config(self, show_popup: bool = True) -> bool:
        try:
            cfg = self._read_config()
            cfg["backends"] = self._extract_backends_from_ui()
            self._write_config(cfg)
            self._log(f"Saved backend wiring to {self.config_path}")
            if show_popup:
                QMessageBox.information(self, "Saved", "Backend wiring saved successfully.")
            return True
        except Exception as e:
            QMessageBox.critical(self, "Save failed", str(e))
            return False

    def _managed_wiring_looks_mismatched(self) -> bool:
        try:
            backends = self._extract_backends_from_ui()
        except Exception:
            return True
        if len(backends) < 4:
            return True
        expected = [("127.0.0.1", 9000 + i) for i in range(4)]
        actual = [(b["host"], int(b["port"])) for b in backends[:4]]
        return actual != expected

    def _setup_timer(self):
        self.timer = QTimer(self)
        self.timer.setInterval(1500)
        self.timer.timeout.connect(self.poll_status)
        self.timer.start()

    def _make_kpi(self, label: str, value: str):
        box = QGroupBox(label)
        layout = QVBoxLayout(box)
        val = QLabel(value)
        val.setStyleSheet("font-size: 24px; font-weight: 700; color: #f8fbff;")
        layout.addWidget(val)
        return {"box": box, "value": val}

    def _log(self, msg: str):
        ts = time.strftime("%H:%M:%S")
        self.logs.append(f"[{ts}] {msg}")

    def _is_port_free(self, host: str, port: int) -> bool:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
            sock.settimeout(0.2)
            return sock.connect_ex((host, port)) != 0

    def _preflight_ports(self) -> bool:
        checks = [("Proxy", "127.0.0.1", 8080)]
        if self.chk_dashboard.isChecked():
            checks.append(("Dashboard", "127.0.0.1", 8501))
        conflicts = []
        for name, host, port in checks:
            if not self._is_port_free(host, port):
                conflicts.append(f"{name} port {port} is already in use")

        if conflicts:
            QMessageBox.warning(self, "Port conflict", "\n".join(conflicts))
            for c in conflicts:
                self._log(f"Preflight failed: {c}")
            return False
        return True

    def _set_online(self, is_online: bool):
        self.status_chip.setText("ONLINE" if is_online else "OFFLINE")
        self.status_chip.setObjectName("ChipOnline" if is_online else "ChipOffline")
        self.status_chip.style().unpolish(self.status_chip)
        self.status_chip.style().polish(self.status_chip)

    def open_dashboard(self):
        webbrowser.open("http://127.0.0.1:8501")

    def open_status(self):
        webbrowser.open(self.status_url)

    def start_stack(self):
        if self.processes:
            self._log("Stack already running.")
            return

        if not self._preflight_ports():
            return

        if not self._save_wiring_to_config(show_popup=False):
            return

        if self.chk_managed_backends.isChecked() and self._managed_wiring_looks_mismatched():
            resp = QMessageBox.question(
                self,
                "Managed backend mismatch",
                "Managed backends are enabled, but backend wiring is not set to 127.0.0.1:9000-9003.\n"
                "Use external mode for real backends, or click 'Load Demo Targets'.\n\n"
                "Continue anyway?",
            )
            if resp != QMessageBox.StandardButton.Yes:
                self._log("Start canceled due to managed backend wiring mismatch.")
                return

        env_proxy = {
            "OMEGA_CONFIG": self.config_path,
            "OMEGA_ADMIN_TOKEN": self.admin_token,
            "OMEGA_ADMIN_ALLOWLIST": self.admin_allowlist,
            "OMEGA_ADMIN_RATE_LIMIT_PER_MIN": self.admin_rate_limit,
        }
        env_loadgen = {"OMEGA_PROXY_URL": self.proxy_url}

        plans = [
            ("proxy", "demo.proxy", env_proxy),
        ]
        if self.chk_managed_backends.isChecked():
            plans.insert(0, ("backends", "demo.backends", None))
        else:
            self._log("Using external backends from wiring table/config (managed backends disabled).")
        if self.chk_loadgen.isChecked():
            plans.append(("loadgen", "demo.loadgen", env_loadgen))
        if self.chk_dashboard.isChecked():
            plans.append(("dashboard", "_internal.dashboard", None))

        self._log("Applying secure local defaults for admin path (token+allowlist+rate-limit).")

        for name, module, env in plans:
            target = self._run_dashboard if name == "dashboard" else _run_module_main
            args = tuple() if name == "dashboard" else (module, env)
            proc = mp.Process(target=target, args=args, daemon=True, name=name)
            proc.start()
            self.processes[name] = proc
            self._log(f"Started {name} (pid={proc.pid})")
        self.runtime_started = True

    def stop_stack(self):
        if not self.processes:
            self._log("No running stack.")
            return

        for name, proc in list(self.processes.items()):
            if proc.is_alive():
                proc.terminate()
                proc.join(timeout=5)
                if proc.is_alive():
                    os.kill(proc.pid, signal.SIGKILL)
                    proc.join(timeout=1)
            self._log(f"Stopped {name}")

        self.processes.clear()
        self.runtime_started = False
        self._set_online(False)

    def _watchdog(self):
        if not self.processes:
            return
        for name, proc in list(self.processes.items()):
            if not proc.is_alive():
                exitcode = proc.exitcode
                self._log(f"Process crash detected: {name} exited with code {exitcode}")
                del self.processes[name]
                QMessageBox.warning(
                    self,
                    "Process exited",
                    f"{name} process exited unexpectedly (code {exitcode}).\nCheck backend wiring/config and restart.",
                )
                break

    def poll_status(self):
        self._watchdog()
        try:
            with urllib.request.urlopen(self.status_url, timeout=1.5) as r:
                snapshot = json.loads(r.read().decode("utf-8"))
            self.last_snapshot = snapshot
            self.render_snapshot(snapshot)
            self._set_online(True)
            if self.runtime_started:
                self.runtime_mode.setText("Security mode: local-only admin control path (enforced)")
        except Exception:
            self._set_online(False)

    def render_snapshot(self, d: dict):
        total = sum(d.get("total_requests", []))
        errors = sum(d.get("total_errors", []))
        err_pct = (errors / total * 100.0) if total else 0.0

        self.kpi_tick["value"].setText(str(d.get("tick", "-")))
        self.kpi_rps["value"].setText(f"{d.get('rps_hist', [0])[-1]:.1f}")
        self.kpi_total["value"].setText(f"{total:,}")
        self.kpi_err["value"].setText(f"{err_pct:.2f}%")
        healthy = sum(1 for h in d.get("health", []) if h)
        self.kpi_health["value"].setText(f"{healthy}/{len(d.get('health', []))}")

        names = d.get("backend_names", [f"backend-{i}" for i in range(len(d.get("health", [])))])
        health = d.get("health", [])
        lat = d.get("latency_hist", [])
        load = d.get("load_hist", [])
        err = d.get("error_hist", [])
        reqs = d.get("total_requests", [])
        vnodes = d.get("vnode_counts", [])
        rates = d.get("rate_limits", [])
        weights = d.get("kan_weights", [])

        n = len(names)
        self.backends_table.setRowCount(n)

        for i in range(n):
            row = [
                names[i],
                "UP" if i < len(health) and health[i] else "DOWN",
                f"{(lat[i][-1] if i < len(lat) and lat[i] else 0):.1f}",
                f"{(load[i][-1] if i < len(load) and load[i] else 0)*100:.1f}%",
                f"{(err[i][-1] if i < len(err) and err[i] else 0)*100:.2f}",
                str(reqs[i] if i < len(reqs) else 0),
                str(int(vnodes[i]) if i < len(vnodes) else 0),
                f"{(rates[i] if i < len(rates) else 0):.0f}",
                f"{(weights[i] if i < len(weights) else 0):.3f}",
            ]
            for c, txt in enumerate(row):
                self.backends_table.setItem(i, c, QTableWidgetItem(txt))

    def closeEvent(self, event):
        self.stop_stack()
        event.accept()


def main():
    mp.freeze_support()
    mp.set_start_method("spawn", force=True)

    app = QApplication(sys.argv)
    win = OmegaDesktop()
    win.show()
    sys.exit(app.exec())


if __name__ == "__main__":
    main()
