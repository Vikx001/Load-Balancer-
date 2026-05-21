import asyncio
import importlib
import json
import multiprocessing as mp
import os
import signal
import sys
import time
import urllib.request
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
    QTableWidget,
    QTableWidgetItem,
    QTextEdit,
    QVBoxLayout,
    QWidget,
)


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

        self.proxy_url = "http://127.0.0.1:8080"
        self.status_url = f"{self.proxy_url}/_omega/status"
        self.config_path = self._resolve_config_path()

        self._build_ui()
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
        self.btn_refresh = QPushButton("Refresh Now")
        self.btn_refresh.clicked.connect(self.poll_status)

        controls.addWidget(self.btn_start)
        controls.addWidget(self.btn_stop)
        controls.addWidget(self.btn_refresh)
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
        info_layout.addWidget(self.runtime_label)
        right_col.addWidget(info_box, 1)

        main.addLayout(right_col, 2)
        root.addLayout(main)

        self.setCentralWidget(central)

        open_proxy = QAction("Open Proxy Status", self)
        open_proxy.triggered.connect(self.poll_status)
        self.menuBar().addAction(open_proxy)

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

    def _set_online(self, is_online: bool):
        self.status_chip.setText("ONLINE" if is_online else "OFFLINE")
        self.status_chip.setObjectName("ChipOnline" if is_online else "ChipOffline")
        self.status_chip.style().unpolish(self.status_chip)
        self.status_chip.style().polish(self.status_chip)

    def start_stack(self):
        if self.processes:
            self._log("Stack already running.")
            return

        env_proxy = {"OMEGA_CONFIG": self.config_path}
        env_loadgen = {"OMEGA_PROXY_URL": self.proxy_url}

        plans = [
            ("backends", "demo.backends", None),
            ("proxy", "demo.proxy", env_proxy),
            ("loadgen", "demo.loadgen", env_loadgen),
        ]

        for name, module, env in plans:
            proc = mp.Process(target=_run_module_main, args=(module, env), daemon=True, name=name)
            proc.start()
            self.processes[name] = proc
            self._log(f"Started {name} (pid={proc.pid})")

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
        self._set_online(False)

    def poll_status(self):
        try:
            with urllib.request.urlopen(self.status_url, timeout=1.5) as r:
                snapshot = json.loads(r.read().decode("utf-8"))
            self.last_snapshot = snapshot
            self.render_snapshot(snapshot)
            self._set_online(True)
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
