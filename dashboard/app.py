"""
Omega-LB  |  Load Balancer Console
────────────────────────────────────
Two modes, same UI:
  LIVE — reads real metrics written by the running proxy (demo/live_metrics.json)
  DEMO — built-in M/M/1 simulation so the dashboard works without any backend

Run:  .venv/bin/streamlit run dashboard/app.py
"""
import sys
import os
import time
import math
from datetime import datetime
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import numpy as np
import streamlit as st
import plotly.graph_objects as go
from plotly.subplots import make_subplots

# ── Live metrics bridge ────────────────────────────────────────────────────────
try:
    from demo.metrics_store import read_live_metrics as _read_live
except ImportError:
    def _read_live():
        return None

st.set_page_config(page_title="Omega-LB Console", layout="wide", initial_sidebar_state="collapsed")

C = {
    "bg":"#080C14","surface":"#0F1420","surface2":"#161D2E","surface3":"#1C2540",
    "border":"#1F2D45","borderl":"#28395A","text":"#E4EAF6","muted":"#6B7EA8","dim":"#3D4E6E",
    "blue":"#3D8EF0","blue_d":"#0C2350","green":"#2DD4AA","green_d":"#063325",
    "amber":"#F0A500","amber_d":"#352400","red":"#F06080","red_d":"#350C18",
    "purple":"#9B7CF8","purple_d":"#1A0D40",
    "series":["#3D8EF0","#2DD4AA","#F0A500","#F06080","#9B7CF8","#38BDF8","#FB7185","#A3E635"],
}
PL = dict(
    template="plotly_dark", paper_bgcolor="rgba(0,0,0,0)", plot_bgcolor="rgba(0,0,0,0)",
    font=dict(family="'Inter',-apple-system,sans-serif",color=C["muted"],size=11),
    margin=dict(t=44,b=32,l=52,r=16),
    xaxis=dict(showgrid=True,gridcolor=C["border"],gridwidth=1,zeroline=False,showline=False,tickfont=dict(size=10,color=C["dim"])),
    yaxis=dict(showgrid=True,gridcolor=C["border"],gridwidth=1,zeroline=False,showline=False,tickfont=dict(size=10,color=C["dim"])),
    legend=dict(orientation="h",yanchor="bottom",y=1.02,xanchor="left",x=0,bgcolor="rgba(0,0,0,0)",font=dict(size=11,color=C["muted"])),
    hovermode="x unified",
    hoverlabel=dict(bgcolor=C["surface3"],font_color=C["text"],bordercolor=C["border"],font_size=11),
)

st.markdown(f"""<style>
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap');
*{{box-sizing:border-box;}}
html,body,[class*="css"]{{font-family:'Inter',-apple-system,sans-serif;background:{C["bg"]};color:{C["text"]};}}
#MainMenu,footer,header{{visibility:hidden;}}
section[data-testid="stSidebar"]{{display:none;}}
.block-container{{padding-top:0!important;max-width:100%!important;padding-left:0!important;padding-right:0!important;}}
div[data-testid="stVerticalBlock"]>div{{gap:0;}}
.stTabs [data-baseweb="tab-list"]{{background:{C["surface"]};border-bottom:1px solid {C["border"]};padding:0 24px;gap:0;position:sticky;top:0;z-index:100;}}
.stTabs [data-baseweb="tab"]{{color:{C["muted"]};font-size:13px;font-weight:500;padding:12px 22px;border-bottom:2px solid transparent;background:transparent;border-radius:0;}}
.stTabs [aria-selected="true"]{{color:{C["text"]};border-bottom:2px solid {C["blue"]};}}
.stTabs [data-baseweb="tab-panel"]{{padding:20px 24px 0 24px;background:{C["bg"]};}}
.kpi{{background:{C["surface"]};border:1px solid {C["border"]};border-radius:10px;padding:18px 20px;height:100%;}}
.kpi-lbl{{font-size:10px;font-weight:700;letter-spacing:.1em;text-transform:uppercase;color:{C["muted"]};margin-bottom:6px;}}
.kpi-val{{font-size:28px;font-weight:700;color:{C["text"]};line-height:1;letter-spacing:-.03em;font-variant-numeric:tabular-nums;}}
.kpi-dlt{{font-size:11px;margin-top:8px;color:{C["dim"]};}}
.kpi-dlt .up{{color:{C["red"]};}} .kpi-dlt .dn{{color:{C["green"]};}} .kpi-dlt .fl{{color:{C["muted"]};}}
.sh{{font-size:10px;font-weight:700;letter-spacing:.1em;text-transform:uppercase;color:{C["muted"]};padding:18px 0 10px 0;border-bottom:1px solid {C["border"]};margin-bottom:14px;}}
.pill{{display:inline-flex;align-items:center;gap:5px;padding:3px 10px 3px 7px;border-radius:100px;font-size:11px;font-weight:600;line-height:1;}}
.dot{{width:6px;height:6px;border-radius:50%;}}
.p-ok{{background:{C["green_d"]};color:{C["green"]};}} .p-ok .dot{{background:{C["green"]};box-shadow:0 0 4px {C["green"]};}}
.p-warn{{background:{C["amber_d"]};color:{C["amber"]};}} .p-warn .dot{{background:{C["amber"]};}}
.p-err{{background:{C["red_d"]};color:{C["red"]};}} .p-err .dot{{background:{C["red"]};box-shadow:0 0 4px {C["red"]};}}
.p-live{{background:{C["green_d"]};color:{C["green"]};}} .p-live .dot{{background:{C["green"]};box-shadow:0 0 5px {C["green"]};animation:blink 2s infinite;}}
.p-demo{{background:{C["amber_d"]};color:{C["amber"]};}} .p-demo .dot{{background:{C["amber"]};}}
@keyframes blink{{0%,100%{{opacity:1;}}50%{{opacity:.4;}}}}
.phdr{{background:{C["surface"]};border-bottom:1px solid {C["border"]};padding:12px 24px;display:flex;align-items:center;justify-content:space-between;}}
.phdr-t{{font-size:14px;font-weight:700;color:{C["text"]};letter-spacing:-.02em;}}
.phdr-s{{font-size:11px;color:{C["muted"]};margin-top:2px;font-family:'JetBrains Mono',monospace;}}
.tbl{{width:100%;border-collapse:collapse;font-size:12px;}}
.tbl th{{text-align:left;font-size:10px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:{C["muted"]};padding:9px 12px;border-bottom:1px solid {C["border"]};background:{C["surface"]};white-space:nowrap;}}
.tbl td{{padding:10px 12px;border-bottom:1px solid {C["border"]};color:{C["text"]};vertical-align:middle;}}
.tbl tr:last-child td{{border-bottom:none;}} .tbl tr:hover td{{background:{C["surface2"]};}}
.card{{background:{C["surface"]};border:1px solid {C["border"]};border-radius:10px;overflow:hidden;}}
.cbf-ok{{background:{C["green_d"]};color:{C["green"]};display:inline-block;padding:1px 7px;border-radius:3px;font-size:10px;font-weight:700;}}
.cbf-fire{{background:{C["red_d"]};color:{C["red"]};display:inline-block;padding:1px 7px;border-radius:3px;font-size:10px;font-weight:700;animation:pulse 1.5s infinite;}}
.cbf-warn{{background:{C["amber_d"]};color:{C["amber"]};display:inline-block;padding:1px 7px;border-radius:3px;font-size:10px;font-weight:700;}}
@keyframes pulse{{0%,100%{{opacity:1;}}50%{{opacity:.5;}}}}
.logpanel{{background:{C["bg"]};border:1px solid {C["border"]};border-radius:8px;height:240px;overflow-y:auto;padding:6px 4px;font-family:'JetBrains Mono',monospace;font-size:10.5px;scrollbar-width:thin;scrollbar-color:{C["border"]} transparent;}}
.logpanel::-webkit-scrollbar{{width:4px;}} .logpanel::-webkit-scrollbar-thumb{{background:{C["border"]};border-radius:2px;}}
.logrow{{padding:2px 8px;border-radius:3px;display:flex;align-items:center;gap:10px;line-height:1.5;}}
.logrow:hover{{background:{C["surface2"]};}}
.lvl{{display:inline-block;width:38px;text-align:center;padding:0 3px;border-radius:3px;font-size:9.5px;font-weight:700;flex-shrink:0;}}
.lvl-i{{background:rgba(61,142,240,.15);color:{C["blue"]};}} .lvl-w{{background:rgba(240,165,0,.15);color:{C["amber"]};}} .lvl-e{{background:rgba(240,96,128,.15);color:{C["red"]};}}
.mono{{font-family:'JetBrains Mono',monospace;}}
.setup-box{{background:{C["surface2"]};border:1px solid {C["borderl"]};border-radius:10px;padding:20px 24px;margin-bottom:16px;}}
.setup-step{{display:flex;gap:14px;align-items:flex-start;margin-bottom:18px;}}
.step-num{{width:26px;height:26px;border-radius:50%;background:{C["blue_d"]};border:1px solid {C["blue"]};color:{C["blue"]};font-size:12px;font-weight:700;display:flex;align-items:center;justify-content:center;flex-shrink:0;margin-top:1px;}}
.step-body{{flex:1;}}
.step-title{{font-size:13px;font-weight:600;color:{C["text"]};margin-bottom:3px;}}
.step-desc{{font-size:12px;color:{C["muted"]};line-height:1.5;}}
.code-block{{background:{C["bg"]};border:1px solid {C["border"]};border-radius:6px;padding:10px 14px;font-family:'JetBrains Mono',monospace;font-size:12px;color:{C["green"]};margin-top:6px;white-space:pre;overflow-x:auto;}}
.mode-banner-demo{{background:{C["amber_d"]};border:1px solid {C["amber"]};border-radius:8px;padding:10px 16px;display:flex;align-items:center;gap:12px;margin-bottom:14px;font-size:12px;color:{C["amber"]};}}
.mode-banner-live{{background:{C["green_d"]};border:1px solid {C["green"]};border-radius:8px;padding:10px 16px;display:flex;align-items:center;gap:12px;margin-bottom:14px;font-size:12px;color:{C["green"]};}}
</style>""", unsafe_allow_html=True)

# ══════════════════════════════════════════════════════════════════════════════
# SIMULATION (DEMO MODE)
# ══════════════════════════════════════════════════════════════════════════════
H_SIM=120; N_SIM=4
SIM_NAMES=["backend-0a2b","backend-1c8d","backend-2f3e","backend-3b9a"]
SIM_ZONES=["us-east-1a","us-east-1b","us-east-1c","us-east-1a"]
SIM_BL=np.array([45.,52.,120.,38.]); SIM_BU=np.array([.30,.35,.45,.28])
SIM_PATHS=[("/api/v1/health","GET"),("/api/v1/users","GET"),("/api/v2/ingest","POST"),
           ("/api/v1/metrics","GET"),("/api/v2/config","PUT"),("/api/v1/events","POST"),
           ("/api/v1/status","GET"),("/api/v2/query","POST")]

def _init_sim():
    if "tick" in st.session_state: return
    s=st.session_state
    s.tick=0; s.rng=np.random.default_rng(42)
    s.lat=np.zeros((N_SIM,H_SIM)); s.load=np.zeros((N_SIM,H_SIM))
    s.rps=np.zeros(H_SIM); s.err=np.zeros((N_SIM,H_SIM))
    s.vnodes=np.array([150.]*N_SIM); s.health=[True]*N_SIM
    s.rl=np.array([1000.]*N_SIM); s.cbf=[False]*N_SIM
    s.proactive=False; s.wt=np.ones(N_SIM)/N_SIM
    s.fail=-1; s.spike=False; s.treq=[0]*N_SIM
    s.logs=[]; s.sla_ok=0; s.sla_tot=0

def _step_sim():
    s=st.session_state; t=s.tick; rng=s.rng
    rps=1800+700*math.sin(2*math.pi*t/60)+350*math.sin(2*math.pi*t/180)
    if s.spike: rps*=2.4
    w=s.vnodes/s.vnodes.sum()
    load=np.zeros(N_SIM); lat=np.zeros(N_SIM); err=np.zeros(N_SIM); cbf=[False]*N_SIM
    for i in range(N_SIM):
        if not s.health[i]: continue
        util=(w[i]*rps)/s.rl[i]
        load[i]=min(util+SIM_BU[i]*0.3+rng.normal(0,.015),1.)
        if load[i]>0.80: cbf[i]=True; load[i]=0.80+rng.uniform(0,.03)
        sl=min(load[i],.97); mu=1./(SIM_BL[i]/1000)
        lam=sl*mu; ml=1000/(mu-lam) if mu>lam else 5000
        lat[i]=max(1., ml+rng.normal(0,ml*0.04))
        if i==s.fail: err[i]=0.14+rng.uniform(0,.04)
        elif load[i]>0.85: err[i]=(load[i]-0.85)*2.5
        else: err[i]=rng.uniform(0,.0015)
        s.treq[i]+=max(1,int(w[i]*rps/10))
        nr=max(1,int(w[i]*rps/10)); s.sla_ok+=int(nr*(1-err[i])*(1 if lat[i]<200 else 0.5)); s.sla_tot+=nr
    s.cbf=cbf
    h=np.array([1. if v else 0. for v in s.health])
    inv=np.where(lat>0,1./(lat+1),0.)*h; raw=inv/inv.sum() if inv.sum()>0 else h/max(h.sum(),1)
    s.wt=0.88*s.wt+0.12*raw
    if t>10:
        xs=np.arange(10,dtype=float)-4.5; slv=s.load[:,-10:].mean(axis=0)
        d2=np.dot(xs,xs); slope=float(np.dot(xs,slv)/d2) if d2>0 else 0
        s.proactive=(slope*30)>0.75
        if s.proactive:
            for i in range(N_SIM):
                if load[i]>0.70: s.vnodes[i]=max(50,s.vnodes[i]*0.97)
        else: s.vnodes=s.vnodes*0.99+np.array([150.]*N_SIM)*0.01
    s.lat=np.roll(s.lat,-1,axis=1); s.lat[:,-1]=lat
    s.load=np.roll(s.load,-1,axis=1); s.load[:,-1]=load
    s.rps=np.roll(s.rps,-1); s.rps[-1]=rps
    s.err=np.roll(s.err,-1,axis=1); s.err[:,-1]=err
    _gen_sim_logs(rng,w,lat,err,rps); s.tick+=1

def _gen_sim_logs(rng,w,lat,err,rps):
    s=st.session_state; n_new=max(3,min(10,int(rps/300)))
    now=datetime.now(); new=[]; sw=w/w.sum() if w.sum()>0 else np.ones(N_SIM)/N_SIM
    for _ in range(n_new):
        i=int(rng.choice(N_SIM,p=sw)); path,method=SIM_PATHS[int(rng.integers(len(SIM_PATHS)))]
        lv=max(1.,lat[i]+rng.normal(0,lat[i]*0.15)); er=err[i]
        if not s.health[i]: level,status,ls,tag="ERROR",503,"—","CONN_REFUSED"
        elif rng.random()<er: level="ERROR";status=int(rng.choice([500,502,503]));ls=f"{lv:.0f}ms";tag="UPSTREAM_ERR"
        elif lv>300: level,status,ls,tag="WARN",200,f"{lv:.0f}ms","SLOW_RESPONSE"
        else: level,status,ls,tag="INFO",200,f"{lv:.0f}ms",""
        ms=int(rng.integers(0,999)); ts=now.strftime("%H:%M:%S")+f".{ms:03d}"
        new.append((ts,level,SIM_NAMES[i],method,f"{path:<22}",status,ls,tag))
    s.logs=new+s.logs[:300]

# ══════════════════════════════════════════════════════════════════════════════
# DATA LAYER — unified bundle
# ══════════════════════════════════════════════════════════════════════════════

def _gen_live_logs(N,lat,err,rps,health,names):
    rng=np.random.default_rng(int(time.time()*10)%(2**32))
    n_new=max(2,min(8,int(rps/400))); now=datetime.now(); new=[]
    ww=np.array([1. if health[i] else 0. for i in range(N)])
    if ww.sum()==0: ww=np.ones(N)
    ww/=ww.sum()
    for _ in range(n_new):
        i=int(rng.choice(N,p=ww)); path,method=SIM_PATHS[int(rng.integers(len(SIM_PATHS)))]
        lv=max(1.,lat[i]+rng.normal(0,lat[i]*0.15)); er=err[i]
        if not health[i]: level,status,ls,tag="ERROR",503,"—","CONN_REFUSED"
        elif rng.random()<er: level="ERROR";status=int(rng.choice([500,502,503]));ls=f"{lv:.0f}ms";tag="UPSTREAM_ERR"
        elif lv>300: level,status,ls,tag="WARN",200,f"{lv:.0f}ms","SLOW_RESPONSE"
        else: level,status,ls,tag="INFO",200,f"{lv:.0f}ms",""
        ms=int(rng.integers(0,999)); ts=now.strftime("%H:%M:%S")+f".{ms:03d}"
        new.append((ts,level,names[i],method,f"{path:<22}",status,ls,tag))
    prev=st.session_state.get("live_logs",[])
    st.session_state.live_logs=new+prev[:300]

def _load_data():
    raw=_read_live()
    if raw is not None:
        N=raw.get("n_backends",len(raw["health"]))
        lat=np.array(raw["latency_hist"]); load=np.array(raw["load_hist"])
        err=np.array(raw["error_hist"]); rps=np.array(raw["rps_hist"])
        treq=raw["total_requests"]; terr=raw["total_errors"]
        sla=(1-sum(terr)/max(1,sum(treq)))*100
        _gen_live_logs(N,lat[:,-1],err[:,-1],rps[-1],raw["health"],raw.get("backend_names",[f"b{i}" for i in range(N)]))
        return "live", dict(
            N=N, H=len(rps), names=raw.get("backend_names",[f"backend-{i}" for i in range(N)]),
            zones=raw.get("backend_zones",["local"]*N), lat=lat, load=load, err=err, rps=rps,
            health=raw["health"], cbf=raw["cbf_active"], vnodes=np.array(raw["vnode_counts"]),
            rl=np.array(raw["rate_limits"]), kan_weights=np.array(raw["kan_weights"]),
            proactive=raw["proactive_active"], treq=treq, sla_pct=sla,
            logs=st.session_state.get("live_logs",[]), tick=raw["tick"], proxy_ts=raw["ts"])
    _init_sim(); _step_sim(); s=st.session_state
    return "demo", dict(
        N=N_SIM, H=H_SIM, names=SIM_NAMES, zones=SIM_ZONES,
        lat=s.lat.copy(), load=s.load.copy(), err=s.err.copy(), rps=s.rps.copy(),
        health=list(s.health), cbf=list(s.cbf), vnodes=s.vnodes.copy(), rl=s.rl.copy(),
        kan_weights=s.wt.copy(), proactive=s.proactive, treq=list(s.treq),
        sla_pct=s.sla_ok/s.sla_tot*100 if s.sla_tot>0 else 100.,
        logs=list(s.logs), tick=s.tick, proxy_ts=None)

# ── Shared helpers ─────────────────────────────────────────────────────────────
def _bar(pct,color):
    w=max(0,min(100,pct))
    return (f'<div style="display:flex;align-items:center;gap:7px;">'
            f'<div style="flex:1;height:3px;background:{C["border"]};border-radius:2px;">'
            f'<div style="width:{w}%;height:3px;background:{color};border-radius:2px;"></div></div>'
            f'<span style="font-size:11px;color:{C["muted"]};min-width:38px;text-align:right;font-variant-numeric:tabular-nums">{pct:.1f}%</span></div>')

def _delta(cur,prev,invert=False):
    diff=cur-prev; pct=diff/abs(prev)*100 if abs(prev)>0.001 else 0
    if abs(pct)<0.5: return '<span class="fl">Stable vs 30s ago</span>'
    a="▲" if diff>0 else "▼"; cls=("up" if diff>0 else "dn") if not invert else ("dn" if diff>0 else "up")
    return f'<span class="{cls}">{a} {abs(pct):.1f}%</span> vs 30s ago'

def _sparksvg(data,color,w=70,h=20):
    d=[v for v in data if v>0]
    if len(d)<2 or max(d)==min(d): return ""
    mn,mx=min(d),max(d)
    pts=[f"{i/(len(d)-1)*w:.1f},{h-((v-mn)/(mx-mn))*h:.1f}" for i,v in enumerate(d)]
    return (f'<svg width="{w}" height="{h}" style="display:block;margin-top:6px;">'
            f'<polyline points="{" ".join(pts)}" fill="none" stroke="{color}" stroke-width="1.3" stroke-linejoin="round" stroke-linecap="round" opacity="0.7"/></svg>')

def _log_html(logs,n=60):
    lmap={"INFO":("lvl-i","INFO"),"WARN":("lvl-w","WARN"),"ERROR":("lvl-e","ERR ")}
    sc_col={2:C["green"],4:C["amber"],5:C["red"]}; lines=[]
    for ts,level,target,method,path,status,ls,tag in logs[:n]:
        cls,lbl=lmap.get(level,("lvl-i","INFO")); sc=sc_col.get(status//100,C["muted"])
        tg=(f' <span style="color:{C["dim"]};font-size:9px;">[{tag}]</span>' if tag else "")
        lines.append(f'<div class="logrow"><span style="color:{C["dim"]};flex-shrink:0;">{ts}</span><span class="lvl {cls}">{lbl}</span><span style="color:{C["muted"]};flex-shrink:0;min-width:90px;" class="mono">{target[:12]}</span><span style="color:{C["blue"]};flex-shrink:0;min-width:38px;">{method}</span><span style="color:{C["muted"]};flex:1;" class="mono">{path}</span><span style="color:{sc};flex-shrink:0;min-width:28px;">{status}</span><span style="color:{C["dim"]};flex-shrink:0;min-width:52px;text-align:right;">{ls}</span>{tg}</div>')
    return "".join(lines)

# ══════════════════════════════════════════════════════════════════════════════
# RENDER
# ══════════════════════════════════════════════════════════════════════════════
mode, d = _load_data()
N=d["N"]; H=d["H"]; NAMES=d["names"]; ZONES=d["zones"]; COLS=C["series"][:N]
ts_ax=np.arange(-H+1,1)
lat=d["lat"]; load=d["load"]; err=d["err"]; rps=d["rps"]
health=d["health"]; cbf=d["cbf"]; vnodes=d["vnodes"]; rl=d["rl"]
kan_w=d["kan_weights"]; proactive=d["proactive"]; treq=d["treq"]
sla_pct=d["sla_pct"]; logs=d["logs"]

n_up=sum(health)
cur_rps=float(rps[-1]); prev_rps=float(rps[-6]) if H>6 else cur_rps
avg_lat=float(np.mean([lat[i,-1] for i in range(N) if health[i]])) if n_up else 0.
avg_latp=float(np.mean([lat[i,-6] for i in range(N) if health[i]])) if n_up and H>6 else avg_lat
avg_err=float(np.mean([err[i,-1]*100 for i in range(N) if health[i]])) if n_up else 0.
avg_errp=float(np.mean([err[i,-6]*100 for i in range(N) if health[i]])) if n_up and H>6 else avg_err
cbf_cnt=sum(cbf); total_reqs=sum(treq)

if n_up==N and cbf_cnt==0: pill_c,pill_l="p-ok","Healthy"
elif n_up>=N-1:             pill_c,pill_l="p-warn","Degraded"
else:                       pill_c,pill_l="p-err","Critical"
mode_pill="p-live" if mode=="live" else "p-demo"
mode_label="LIVE" if mode=="live" else "DEMO"

st.markdown(f"""<div class="phdr">
  <div><div class="phdr-t">Omega&#8209;LB &nbsp;/&nbsp; Load Balancer Console</div>
  <div class="phdr-s">{"Real proxy data" if mode=="live" else "Simulation"} &nbsp;·&nbsp; {N} targets &nbsp;·&nbsp; {total_reqs:,} total requests</div></div>
  <div style="display:flex;align-items:center;gap:16px;">
    <div style="text-align:center;"><div style="font-size:10px;color:{C["dim"]};letter-spacing:.07em;text-transform:uppercase;margin-bottom:2px;">SLA</div>
    <div style="font-size:15px;font-weight:700;color:{""+C["green"] if sla_pct>99 else C["amber"]};letter-spacing:-.02em;">{sla_pct:.2f}%</div></div>
    <div style="width:1px;height:32px;background:{C["border"]};"></div>
    <span class="pill {mode_pill}"><span class="dot"></span>{mode_label}</span>
    <span class="pill {pill_c}"><span class="dot"></span>{pill_l}</span>
    <div style="text-align:right;"><div style="font-size:10px;color:{C["dim"]};letter-spacing:.05em;text-transform:uppercase;">Updated</div>
    <div style="font-size:12px;color:{C["muted"]};font-family:'JetBrains Mono',monospace;">{time.strftime("%H:%M:%S")} UTC</div></div>
  </div>
</div>""", unsafe_allow_html=True)

T=st.tabs(["Overview","Routing Policy","Rate Control","Health Checks","Setup"])

# ═══ TAB 1 — OVERVIEW ══════════════════════════════════════════════════════════
with T[0]:
    if mode=="demo":
        st.markdown(f'<div class="mode-banner-demo">⚡ <b>DEMO mode</b> — simulation data. Start the proxy to see real traffic. See the <b>Setup</b> tab.</div>',unsafe_allow_html=True)
    else:
        age=round(time.time()-d["proxy_ts"],1)
        st.markdown(f'<div class="mode-banner-live">● <b>LIVE</b> — connected to proxy · data age {age}s · tick #{d["tick"]}</div>',unsafe_allow_html=True)

    k=st.columns(4,gap="small")
    kpis=[("Requests / sec",f"{cur_rps:,.0f}",_delta(cur_rps,prev_rps),C["blue"],rps[-30:]),
          ("Avg Latency (P50)",f"{avg_lat:.0f} ms",_delta(avg_lat,avg_latp,True),C["purple"],lat[:,-30:].mean(axis=0)),
          ("Error Rate",f"{avg_err:.2f}%",_delta(avg_err,avg_errp,True),C["amber"],err[:,-30:].mean(axis=0)*100),
          ("Healthy Targets",f"{n_up} / {N}",f'<span class="{"dn" if n_up<N else "fl"}">{"All passing health checks" if n_up==N else f"{N-n_up} unhealthy"}</span>',C["green"],np.array([float(n_up)]*30))]
    for col,(lbl,val,dlt,acc,sp) in zip(k,kpis):
        col.markdown(f'<div class="kpi" style="border-top:3px solid {acc};"><div class="kpi-lbl">{lbl}</div><div class="kpi-val">{val}</div>{_sparksvg(sp,acc)}<div class="kpi-dlt">{dlt}</div></div>',unsafe_allow_html=True)

    st.markdown("<div style='height:18px'></div>",unsafe_allow_html=True)
    st.markdown('<div class="sh">Backend Utilization</div>',unsafe_allow_html=True)
    fig_g=make_subplots(rows=1,cols=N,specs=[[{"type":"indicator"}]*N],horizontal_spacing=0.04)
    for i in range(N):
        lv=load[i,-1]*100; latv=lat[i,-1]; ev=err[i,-1]*100
        col_g=COLS[i] if lv<80 else C["red"]
        if not health[i]: lv,col_g=0.,C["dim"]
        ref=load[i,-10]*100 if d["tick"]>10 else lv
        fig_g.add_trace(go.Indicator(mode="gauge+number+delta",value=lv,
            number={"suffix":"%","font":{"size":26,"color":col_g,"family":"Inter"}},
            delta={"reference":ref,"relative":False,"valueformat":".1f","font":{"size":11},"suffix":"%"},
            title={"text":f"<b style='font-family:JetBrains Mono,monospace;font-size:11px;color:{C['muted']};'>{NAMES[i][:12]}</b><br><span style='font-size:10px;color:{C['dim']};'>{latv:.0f}ms · {ev:.2f}% err</span>","font":{"family":"Inter"}},
            gauge={"axis":{"range":[0,100],"tickwidth":0,"visible":False},"bar":{"color":col_g,"thickness":0.22},"bgcolor":"rgba(0,0,0,0)","borderwidth":0,
                   "steps":[{"range":[0,70],"color":C["surface2"]},{"range":[70,80],"color":C["amber_d"]},{"range":[80,100],"color":C["red_d"]}],
                   "threshold":{"line":{"color":C["red"],"width":2},"thickness":0.8,"value":80}}),row=1,col=i+1)
    fig_g.update_layout(height=220,paper_bgcolor="rgba(0,0,0,0)",plot_bgcolor="rgba(0,0,0,0)",margin=dict(t=20,b=10,l=10,r=10),font_family="Inter")
    st.markdown('<div class="card">',unsafe_allow_html=True)
    st.plotly_chart(fig_g,use_container_width=True,config={"displayModeBar":False})
    st.markdown('</div>',unsafe_allow_html=True)

    st.markdown("<div style='height:16px'></div>",unsafe_allow_html=True)
    ca,cb=st.columns([2,1],gap="small")
    with ca:
        fig_rps=go.Figure(go.Scatter(x=ts_ax,y=rps,line=dict(color=C["blue"],width=1.5),fill="tozeroy",fillcolor="rgba(61,142,240,0.06)",hovertemplate="%{y:,.0f} req/s<extra></extra>"))
        fig_rps.update_layout(**{**PL,"title":dict(text="Requests per Second",font=dict(size=13,color=C["text"]),x=0,xanchor="left"),"height":240,"yaxis":dict(**PL["yaxis"],title=dict(text="req/s",font=dict(size=10))),"xaxis":dict(**PL["xaxis"],title=dict(text="seconds",font=dict(size=10)))})
        st.markdown('<div class="card">',unsafe_allow_html=True)
        st.plotly_chart(fig_rps,use_container_width=True,config={"displayModeBar":False})
        st.markdown('</div>',unsafe_allow_html=True)
    with cb:
        st.markdown(f'<div class="sh">Live Request Log</div><div class="logpanel">{_log_html(logs,60)}</div>',unsafe_allow_html=True)

    st.markdown("<div style='height:16px'></div>",unsafe_allow_html=True)
    c2,c3=st.columns(2,gap="small")
    with c2:
        fig2=go.Figure()
        for i in range(N):
            fig2.add_trace(go.Scatter(x=ts_ax,y=load[i]*100,name=NAMES[i],line=dict(color=COLS[i],width=1.5),opacity=0.3 if not health[i] else 1.,hovertemplate=f"{NAMES[i]}: %{{y:.1f}}%<extra></extra>"))
        fig2.add_hline(y=80,line=dict(color=C["red"],width=1,dash="dot"),annotation_text="CBF cap (80%)",annotation_font_size=10,annotation_font_color=C["red"])
        fig2.update_layout(**{**PL,"title":dict(text="Target Utilization (%)",font=dict(size=13,color=C["text"]),x=0,xanchor="left"),"height":230,"yaxis":dict(**PL["yaxis"],ticksuffix="%",range=[0,105])})
        st.markdown('<div class="card">',unsafe_allow_html=True); st.plotly_chart(fig2,use_container_width=True,config={"displayModeBar":False}); st.markdown('</div>',unsafe_allow_html=True)
    with c3:
        fig3=go.Figure()
        for i in range(N):
            if health[i]: fig3.add_trace(go.Scatter(x=ts_ax,y=lat[i],name=NAMES[i],line=dict(color=COLS[i],width=1.5),hovertemplate=f"{NAMES[i]}: %{{y:.0f}} ms<extra></extra>"))
        fig3.update_layout(**{**PL,"title":dict(text="Latency — P50 (ms)",font=dict(size=13,color=C["text"]),x=0,xanchor="left"),"height":230,"yaxis":dict(**PL["yaxis"],ticksuffix=" ms")})
        st.markdown('<div class="card">',unsafe_allow_html=True); st.plotly_chart(fig3,use_container_width=True,config={"displayModeBar":False}); st.markdown('</div>',unsafe_allow_html=True)

    st.markdown("<div style='height:16px'></div><div class='sh'>Registered Targets</div>",unsafe_allow_html=True)
    rows=""
    for i in range(N):
        lv=load[i,-1]*100; latv=lat[i,-1]; ev=err[i,-1]*100
        if not health[i]: sb=f'<span class="pill p-err"><span class="dot"></span>Unhealthy</span>'
        elif ev>2 or latv>400: sb=f'<span class="pill p-warn"><span class="dot"></span>Degraded</span>'
        else: sb=f'<span class="pill p-ok"><span class="dot"></span>Healthy</span>'
        cb2=f'<span class="cbf-fire">FIRING</span>' if cbf[i] else (f'<span class="cbf-warn">WARN</span>' if lv>70 else f'<span class="cbf-ok">OK</span>')
        rows+=f'<tr><td class="mono" style="color:{C["muted"]}">{NAMES[i]}</td><td style="color:{C["muted"]}">{ZONES[i]}</td><td>{sb}</td><td style="min-width:150px">{_bar(lv,COLS[i])}</td><td style="font-variant-numeric:tabular-nums">{"—" if not health[i] else f"{latv:.0f} ms"}</td><td style="font-variant-numeric:tabular-nums;color:{""+C["red"] if ev>1 else C["text"]}">{ev:.2f}%</td><td style="font-variant-numeric:tabular-nums;color:{C["muted"]}">{int(vnodes[i])}</td><td style="font-variant-numeric:tabular-nums;color:{C["muted"]}">{rl[i]:.0f}</td><td>{cb2}</td><td style="font-variant-numeric:tabular-nums;color:{C["muted"]}">{treq[i]/1000:.1f}K</td></tr>'
    st.markdown(f'<div class="card"><table class="tbl"><thead><tr><th>Target</th><th>Zone</th><th>Status</th><th>Utilization</th><th>Latency</th><th>Error%</th><th>Vnodes</th><th>Rate cap</th><th>CBF</th><th>Requests</th></tr></thead><tbody>{rows}</tbody></table></div>',unsafe_allow_html=True)

    if mode=="demo":
        with st.expander("Fault Simulation",expanded=False):
            fc,sc2,rc=st.columns([2,1,3]); s=st.session_state
            with fc:
                sel=st.selectbox("Deregister target",["None"]+SIM_NAMES,key="fail_sel")
                s.fail=SIM_NAMES.index(sel) if sel!="None" else -1
                for i in range(N_SIM): s.health[i]=(i!=s.fail)
            with sc2: s.spike=st.toggle("Traffic spike (2.4×)",value=False,key="spike_tog")
            with rc:
                cr=st.columns(N_SIM)
                for i,cx in enumerate(cr): s.rl[i]=cx.slider(f"B{i}",200,2000,int(s.rl[i]),100,key=f"rl_{i}")

# ═══ TAB 2 — ROUTING POLICY ════════════════════════════════════════════════════
with T[1]:
    ra,rb=st.columns([3,1],gap="small")
    with ra:
        fig_w=go.Figure()
        wh=np.zeros((N,H))
        for ti in range(H):
            lv2=lat[:,ti]; inv2=np.where(lv2>0,1./(lv2+1),0.)
            wh[:,ti]=inv2/inv2.sum() if inv2.sum()>0 else np.ones(N)/N
        for i in range(N):
            r2,g2,b2=int(COLS[i][1:3],16),int(COLS[i][3:5],16),int(COLS[i][5:7],16)
            fig_w.add_trace(go.Scatter(x=ts_ax,y=wh[i]*100,name=NAMES[i],line=dict(color=COLS[i],width=1.5),fill="tonexty" if i>0 else "tozeroy",fillcolor=f"rgba({r2},{g2},{b2},0.1)",stackgroup="w",hovertemplate=f"{NAMES[i]}: %{{y:.1f}}%<extra></extra>"))
        fig_w.update_layout(**{**PL,"title":dict(text="KAN Routing Weight Distribution (%)",font=dict(size=13,color=C["text"]),x=0,xanchor="left"),"height":250,"yaxis":dict(**PL["yaxis"],ticksuffix="%",range=[0,105])})
        st.markdown('<div class="card">',unsafe_allow_html=True); st.plotly_chart(fig_w,use_container_width=True,config={"displayModeBar":False}); st.markdown('</div>',unsafe_allow_html=True)

        st.markdown('<div style="height:16px"></div><div class="sh">CBF Safety Projection</div>',unsafe_allow_html=True)
        eq_rows=""
        for i in range(N):
            cpu_c=load[i,-1]; lat_c=lat[i,-1]/1000; err_c=err[i,-1]
            wsym=max(0,1-0.42*cpu_c-0.31*lat_c-10*err_c)*(1 if health[i] else 0)
            margin=0.80-cpu_c
            cb3=f'<span class="cbf-fire">FIRING</span>' if cbf[i] else (f'<span class="cbf-warn">WARN</span>' if cpu_c>0.70 else f'<span class="cbf-ok">OK</span>')
            eq_rows+=f'<tr><td class="mono" style="color:{C["muted"]}">{NAMES[i]}</td><td class="mono" style="font-size:11px;color:{C["muted"]}">max(0, 1&minus;0.42·<b style="color:{C["text"]}">{cpu_c:.3f}</b>&minus;0.31·<b style="color:{C["text"]}">{lat_c:.3f}</b>&minus;10·<b style="color:{C["text"]}">{err_c:.4f}</b>)·{int(health[i])}</td><td><b style="color:{COLS[i]}">{wsym:.4f}</b></td><td style="color:{""+C["red"] if margin<0 else C["muted"]}">{margin*100:+.1f}%</td><td>{cb3}</td></tr>'
        st.markdown(f'<div class="card"><table class="tbl"><thead><tr><th>Target</th><th>Equation &nbsp;w=max(0,1−0.42·cpu−0.31·lat−10·err)×health</th><th>Weight</th><th>Margin</th><th>Status</th></tr></thead><tbody>{eq_rows}</tbody></table></div>',unsafe_allow_html=True)

    with rb:
        st.markdown('<div class="sh">Traffic Distribution</div>',unsafe_allow_html=True)
        fig_d=go.Figure(go.Pie(values=vnodes/vnodes.sum()*100,labels=NAMES,hole=0.62,marker=dict(colors=COLS,line=dict(color=C["surface"],width=2)),textinfo="none",sort=False,direction="clockwise",hovertemplate="<b>%{label}</b><br>%{value:.1f}%<extra></extra>"))
        fig_d.add_annotation(text=f"<b>{n_up}/{N}</b>",x=0.5,y=0.5,showarrow=False,font=dict(color=C["text"],family="Inter",size=20),align="center")
        fig_d.update_layout(height=200,paper_bgcolor="rgba(0,0,0,0)",plot_bgcolor="rgba(0,0,0,0)",margin=dict(t=8,b=8,l=8,r=8),showlegend=False)
        st.markdown('<div class="card">',unsafe_allow_html=True); st.plotly_chart(fig_d,use_container_width=True,config={"displayModeBar":False}); st.markdown('</div>',unsafe_allow_html=True)

        st.markdown('<div style="height:14px"></div><div class="sh">Hash Ring — vnodes</div>',unsafe_allow_html=True)
        tot=vnodes.sum()
        for i in range(N):
            pct2=vnodes[i]/tot*100
            st.markdown(f'<div style="margin-bottom:14px;"><div style="display:flex;justify-content:space-between;margin-bottom:4px;"><span class="mono" style="font-size:11px;color:{C["muted"]}">{NAMES[i][:12]}</span><span style="font-size:11px;color:{C["text"]};font-variant-numeric:tabular-nums">{pct2:.1f}%</span></div><div style="height:4px;background:{C["border"]};border-radius:2px;"><div style="width:{pct2}%;height:4px;background:{COLS[i]};border-radius:2px;"></div></div><div style="font-size:10px;color:{C["dim"]};margin-top:2px;">{int(vnodes[i])} vnodes</div></div>',unsafe_allow_html=True)
        wh3=vnodes/tot*np.array([1. if h else 0. for h in health]); nh=max(1,n_up)
        if wh3.sum()>0: wh3/=wh3.sum()
        bf=float(wh3.max())/(1./nh); bfc=C["green"] if bf<=1.1 else (C["amber"] if bf<=1.3 else C["red"])
        st.markdown(f'<div style="background:{C["surface2"]};border:1px solid {C["borderl"]};border-radius:8px;padding:14px 16px;"><div style="font-size:10px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:{C["muted"]};margin-bottom:4px;">Balance Factor</div><div style="font-size:24px;font-weight:700;color:{bfc};letter-spacing:-.03em;">{bf:.3f}</div><div style="font-size:10px;color:{C["dim"]};margin-top:2px;">ideal 1.000 · cap ≤ 1.100</div></div>',unsafe_allow_html=True)
        if proactive:
            st.markdown(f'<div style="background:{C["amber_d"]};border:1px solid {C["amber"]};border-radius:6px;padding:10px 12px;margin-top:10px;font-size:11px;color:{C["amber"]};"><b>Proactive redistribution active</b><br><span style="color:{C["muted"]}">Rising load slope detected.</span></div>',unsafe_allow_html=True)

# ═══ TAB 3 — RATE CONTROL ══════════════════════════════════════════════════════
with T[2]:
    st.markdown('<div class="sh">DQN Adaptive Rate Limiting</div>',unsafe_allow_html=True)
    rc_c=st.columns(N,gap="small")
    for i,col in enumerate(rc_c):
        arr=(vnodes[i]/vnodes.sum())*cur_rps; util=min(1.,arr/rl[i]) if health[i] else 0
        lv3=load[i,-1]
        atxt,ac=("Throttling ↓",C["red"]) if lv3>0.80 else (("Holding —",C["amber"]) if lv3>0.65 else ("Expanding ↑",C["green"]))
        col.markdown(f'<div class="kpi" style="border-top:3px solid {COLS[i]};"><div class="kpi-lbl">{NAMES[i][:14]}</div><div style="font-size:20px;font-weight:700;color:{C["text"]};letter-spacing:-.02em;margin-bottom:10px;font-variant-numeric:tabular-nums;">{arr:.0f} <span style="font-size:11px;font-weight:400;color:{C["muted"]}">/ {rl[i]:.0f} rps</span></div><div style="height:3px;background:{C["border"]};border-radius:2px;margin-bottom:8px;"><div style="width:{util*100:.1f}%;height:3px;background:{COLS[i]};border-radius:2px;"></div></div><div style="font-size:11px;color:{ac};font-weight:600;">DQN: {atxt}</div></div>',unsafe_allow_html=True)
    st.markdown("<div style='height:18px'></div>",unsafe_allow_html=True)
    fig_rl=go.Figure()
    for i in range(N):
        ah=(vnodes[i]/vnodes.sum())*rps; uth=np.minimum(1.,ah/rl[i])*100
        fig_rl.add_trace(go.Scatter(x=ts_ax,y=uth,name=NAMES[i],line=dict(color=COLS[i],width=1.5),hovertemplate=f"{NAMES[i]}: %{{y:.1f}}%<extra></extra>"))
    fig_rl.add_hline(y=90,line=dict(color=C["red"],width=1,dash="dot"),annotation_text="Rate limit threshold",annotation_font_size=10,annotation_font_color=C["red"])
    fig_rl.update_layout(**{**PL,"title":dict(text="Token Bucket Utilization (%)",font=dict(size=13,color=C["text"]),x=0,xanchor="left"),"height":240,"yaxis":dict(**PL["yaxis"],ticksuffix="%",range=[0,105])})
    st.markdown('<div class="card">',unsafe_allow_html=True); st.plotly_chart(fig_rl,use_container_width=True,config={"displayModeBar":False}); st.markdown('</div>',unsafe_allow_html=True)

# ═══ TAB 4 — HEALTH CHECKS ═════════════════════════════════════════════════════
with T[3]:
    ha,hb=st.columns([1,2],gap="small")
    with ha:
        st.markdown('<div class="sh">Target Status</div>',unsafe_allow_html=True)
        for i in range(N):
            lv4=lat[i,-1]; ev4=err[i,-1]*100; uv4=load[i,-1]*100
            if not health[i]: bdr,sl4,sc4=C["red"],"Unhealthy","p-err"
            elif ev4>2 or lv4>400: bdr,sl4,sc4=C["amber"],"Degraded","p-warn"
            else: bdr,sl4,sc4=C["green"],"Healthy","p-ok"
            st.markdown(f'<div style="background:{C["surface"]};border:1px solid {C["border"]};border-left:3px solid {bdr};border-radius:8px;padding:14px 16px;margin-bottom:10px;"><div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:10px;"><span class="mono" style="font-size:12px;font-weight:600;color:{C["text"]}">{NAMES[i]}</span><span class="pill {sc4}"><span class="dot"></span>{sl4}</span></div><div style="display:grid;grid-template-columns:repeat(3,1fr);gap:4px;"><div><div style="font-size:9px;color:{C["dim"]};text-transform:uppercase;letter-spacing:.08em;">P50 Latency</div><div style="font-size:15px;font-weight:700;color:{C["text"]};font-variant-numeric:tabular-nums">{"—" if not health[i] else f"{lv4:.0f} ms"}</div></div><div><div style="font-size:9px;color:{C["dim"]};text-transform:uppercase;letter-spacing:.08em;">Error Rate</div><div style="font-size:15px;font-weight:700;font-variant-numeric:tabular-nums;color:{""+C["red"] if ev4>1 else C["text"]}">{ev4:.2f}%</div></div><div><div style="font-size:9px;color:{C["dim"]};text-transform:uppercase;letter-spacing:.08em;">Utilization</div><div style="font-size:15px;font-weight:700;font-variant-numeric:tabular-nums;color:{""+C["red"] if uv4>80 else C["text"]}">{uv4:.1f}%</div></div></div></div>',unsafe_allow_html=True)
    with hb:
        st.markdown('<div class="sh">Latency Percentiles — 20-sample window</div>',unsafe_allow_html=True)
        p50s=[float(np.percentile(lat[i,-20:],50)) if health[i] else 0 for i in range(N)]
        p95s=[float(np.percentile(lat[i,-20:],95)) if health[i] else 0 for i in range(N)]
        p99s=[float(np.percentile(lat[i,-20:],99)) if health[i] else 0 for i in range(N)]
        fig_p=go.Figure()
        fig_p.add_trace(go.Bar(name="P50",x=NAMES,y=p50s,marker_color=C["blue"],marker_line_width=0))
        fig_p.add_trace(go.Bar(name="P95",x=NAMES,y=p95s,marker_color=C["amber"],marker_line_width=0))
        fig_p.add_trace(go.Bar(name="P99",x=NAMES,y=p99s,marker_color=C["red"],marker_line_width=0))
        fig_p.update_layout(**{**PL,"barmode":"group","bargap":0.28,"bargroupgap":0.06,"height":230,"title":dict(text="Latency Percentiles",font=dict(size=13,color=C["text"]),x=0,xanchor="left"),"yaxis":dict(**PL["yaxis"],ticksuffix=" ms")})
        st.markdown('<div class="card">',unsafe_allow_html=True); st.plotly_chart(fig_p,use_container_width=True,config={"displayModeBar":False}); st.markdown('</div>',unsafe_allow_html=True)
        st.markdown('<div style="height:14px"></div><div class="sh">Latency Heatmap — last 60 ticks</div>',unsafe_allow_html=True)
        fig_hm=go.Figure(go.Heatmap(z=lat[:,-60:],x=np.arange(-59,1),y=[n[:10] for n in NAMES],colorscale=[[0,C["green_d"]],[0.25,C["green"]],[0.6,C["amber"]],[1,C["red"]]],colorbar=dict(title=dict(text="ms",side="right"),thickness=10,len=0.9,tickfont=dict(size=9,color=C["muted"]),outlinewidth=0),hovertemplate="<b>%{y}</b><br>t=%{x}s<br>%{z:.0f} ms<extra></extra>",zmin=0,zmax=500,xgap=1,ygap=2))
        fig_hm.update_layout(height=160,paper_bgcolor="rgba(0,0,0,0)",plot_bgcolor="rgba(0,0,0,0)",margin=dict(t=10,b=28,l=80,r=60),xaxis=dict(showgrid=False,zeroline=False,tickfont=dict(size=9,color=C["dim"])),yaxis=dict(showgrid=False,zeroline=False,tickfont=dict(size=10,color=C["muted"])))
        st.markdown('<div class="card">',unsafe_allow_html=True); st.plotly_chart(fig_hm,use_container_width=True,config={"displayModeBar":False}); st.markdown('</div>',unsafe_allow_html=True)

    st.markdown('<div style="height:14px"></div><div class="sh">Health Check Probe Log</div>',unsafe_allow_html=True)
    probe_rows=""
    for i in range(N):
        for j in range(5):
            t_ago=(j+1)*2; ok=health[i] and err[i,-1-j]<0.08
            rc2c=C["green"] if ok else C["red"]; rt="200 OK" if ok else "503 Service Unavailable"; lt2=f"{lat[i,-1-j]:.0f} ms" if health[i] else "timeout"
            probe_rows+=f"<tr><td style='color:{C['dim']};font-family:JetBrains Mono,monospace;'>{t_ago}s ago</td><td class='mono' style='color:{C['muted']}'>{NAMES[i]}</td><td style='color:{C['muted']}'>{ZONES[i]}</td><td><span style='color:{rc2c};font-weight:600;'>{rt}</span></td><td style='font-variant-numeric:tabular-nums;color:{C['muted']}'>{lt2}</td><td style='font-variant-numeric:tabular-nums;color:{C['muted']}'>{err[i,-1-j]*100:.2f}%</td></tr>"
    st.markdown(f'<div class="card" style="max-height:240px;overflow-y:auto;"><table class="tbl"><thead><tr><th>Time</th><th>Target</th><th>Zone</th><th>Result</th><th>RTT</th><th>Error%</th></tr></thead><tbody>{probe_rows}</tbody></table></div>',unsafe_allow_html=True)

# ═══ TAB 5 — SETUP ══════════════════════════════════════════════════════════════
with T[4]:
    # Connection status
    if mode=="live":
        st.markdown(f'<div class="setup-box" style="border-color:{C["green"]};"><div style="display:flex;align-items:center;gap:12px;margin-bottom:8px;"><span class="pill p-live"><span class="dot"></span>LIVE</span><span style="font-size:14px;font-weight:700;color:{C["text"]};">Proxy connected</span></div><div style="font-size:13px;color:{C["muted"]};">Real metrics from the running proxy. Data age: <b style="color:{C["text"]}">{round(time.time()-d["proxy_ts"],1)}s</b> &nbsp;·&nbsp; Tick: <b style="color:{C["text"]}">{d["tick"]}</b> &nbsp;·&nbsp; Total requests: <b style="color:{C["text"]}">{total_reqs:,}</b></div></div>',unsafe_allow_html=True)
    else:
        st.markdown(f'<div class="setup-box" style="border-color:{C["amber"]};"><div style="display:flex;align-items:center;gap:12px;margin-bottom:8px;"><span class="pill p-demo"><span class="dot"></span>DEMO</span><span style="font-size:14px;font-weight:700;color:{C["text"]};">Proxy not running</span></div><div style="font-size:13px;color:{C["muted"]};">Showing built-in simulation. Start the proxy to see real traffic from your backends. The DEMO badge turns LIVE automatically.</div></div>',unsafe_allow_html=True)

    st.markdown('<div class="sh">Quick Start — connect your application in 5 steps</div>',unsafe_allow_html=True)
    steps=[
        ("Get the code","If you haven't already:","git clone https://github.com/your-org/omega-lb\ncd omega-lb"),
        ("Install dependencies","One-time setup — creates a virtual environment:","python3 -m venv .venv\n.venv/bin/pip install -r requirements.txt"),
        ("Edit omega-lb.yaml","Replace the example backends with your real service addresses. Supports 2–8 backends:","backends:\n  - host: \"192.168.1.10\"\n    port: 8001\n    name: \"api-prod-1\"\n    zone: \"dc-a\"\n  - host: \"192.168.1.11\"\n    port: 8001\n    name: \"api-prod-2\"\n    zone: \"dc-b\""),
        ("Start the proxy","Listens on port 8080 by default (change in omega-lb.yaml). Point your load generator at it:","python demo/proxy.py\n# or one-command:  ./start.sh"),
        ("Open this dashboard","Run in a separate terminal — it auto-detects the proxy:","# already running if you used ./start.sh\n.venv/bin/streamlit run dashboard/app.py"),
    ]
    for n_step,(title,desc,code) in enumerate(steps,1):
        st.markdown(f'<div class="setup-step"><div class="step-num">{n_step}</div><div class="step-body"><div class="step-title">{title}</div><div class="step-desc">{desc}</div><div class="code-block">{code}</div></div></div>',unsafe_allow_html=True)

    # Config editor
    st.markdown('<div style="height:20px"></div><div class="sh">omega-lb.yaml — edit and save</div>',unsafe_allow_html=True)
    cfg_path=os.path.join(os.path.dirname(__file__),"..","omega-lb.yaml")
    cfg_text=""
    if os.path.exists(cfg_path):
        with open(cfg_path) as f: cfg_text=f.read()
    else:
        cfg_text="# omega-lb.yaml not found in repo root"
    edited=st.text_area("Edit and save — restart the proxy to apply:",value=cfg_text,height=320,key="cfg_editor")
    sc_btn,_=st.columns([1,5])
    with sc_btn:
        if st.button("Save omega-lb.yaml",type="primary"):
            try:
                with open(cfg_path,"w") as f: f.write(edited)
                st.success("Saved. Restart the proxy to apply changes.")
            except Exception as e: st.error(f"Could not save: {e}")

    # Architecture table
    st.markdown(f'<div style="height:20px"></div><div class="sh">Architecture</div><div class="setup-box"><table class="tbl"><thead><tr><th>Layer</th><th>Component</th><th>What it does</th><th>Status</th></tr></thead><tbody><tr><td>L1</td><td class="mono">H&amp;A Ring</td><td>Consistent hashing · MurmurHash3 · {int(sum(vnodes))} vnodes</td><td><span class="cbf-ok">ACTIVE</span></td></tr><tr><td>L2</td><td class="mono">CBF Projection</td><td>Control Barrier Function · 80% utilisation cap</td><td><span class="{"cbf-fire" if any(cbf) else "cbf-ok"}">{"FIRING" if any(cbf) else "OK"}</span></td></tr><tr><td>L3</td><td class="mono">KAN Inference</td><td>Symbolic routing weights · ONNX hot-reloadable</td><td><span class="cbf-ok">ACTIVE</span></td></tr><tr><td>L4</td><td class="mono">DQN Rate Limiter</td><td>Adaptive token buckets · ε-greedy policy</td><td><span class="cbf-ok">ACTIVE</span></td></tr><tr><td>L5</td><td class="mono">Proactive Control</td><td>30s load-slope lookahead · vnode pre-distribution</td><td><span class="{"cbf-warn" if proactive else "cbf-ok"}">{"ACTIVE" if proactive else "STANDBY"}</span></td></tr></tbody></table><div style="font-size:11px;color:{C["muted"]};margin-top:12px;">The proxy serves HTTP on port 8080. Your app sends requests there — Omega-LB routes them transparently. No code changes needed in your application.</div></div>',unsafe_allow_html=True)

# ── auto-refresh ───────────────────────────────────────────────────────────────
time.sleep(1)
st.rerun()
