#!/usr/bin/env python3
"""Generates the .dc.html artboards for the tablepluslike design canvas.

Edit this file, re-run it, then re-seed the canvas. Hand-editing the
generated .dc.html files works too but will be overwritten on the next run.
"""
import json
import pathlib

OUT = pathlib.Path(__file__).parent

FONTS = ('<link rel="stylesheet" href="https://fonts.googleapis.com/css2?'
         'family=IBM+Plex+Sans:wght@400;500;600&family=IBM+Plex+Mono:wght@400;500&display=swap">')

LIGHT = """
  --bg:#f2f1ee; --surface:#fbfaf8; --alt:#f6f5f2; --line:#dedbd5; --hair:#eae7e2;
  --text:#23211e; --dim:#6f6a63; --faint:#7d766d;
  --accent:#24707a; --accent-bg:#deeef0; --sel:#e7f0f0;
  --danger:#9e4436; --danger-bg:#f6e6e2;
  --warn:#a8792c; --warn-bg:#f7eeda;
  --ok:#3d7d55; --ok-bg:#e3efe6;
"""

DARK = """
  --bg:#1b1a18; --surface:#212020; --alt:#262523; --line:#35322e; --hair:#2b2926;
  --text:#e9e6e0; --dim:#9b948b; --faint:#7e776f;
  --accent:#57aab2; --accent-bg:#173a3c; --sel:#1e3335;
  --danger:#c9705d; --danger-bg:#3a221d;
  --warn:#c99a4e; --warn-bg:#382c17;
  --ok:#6aab80; --ok-bg:#1e3325;
"""

BASE = """
*{box-sizing:border-box;}
body{margin:0;font-family:'IBM Plex Sans',system-ui,-apple-system,'Segoe UI',sans-serif;
 font-size:12px;color:var(--text);background:var(--bg);-webkit-font-smoothing:antialiased;}
a{color:var(--accent);text-decoration:none;} a:hover{color:var(--text);}
.m{font-family:'IBM Plex Mono',ui-monospace,Menlo,'Cascadia Mono',monospace;
 font-variant-ligatures:none;}
.app{display:flex;flex-direction:column;overflow:hidden;background:var(--bg);}
.tbar{flex:0 0 38px;display:flex;align-items:center;gap:14px;padding:0 12px;
 border-bottom:1px solid var(--line);}
.lights{display:flex;gap:8px;}
.lt{width:11px;height:11px;border-radius:50%;}
.body{flex:1;display:flex;min-height:0;}
.side{flex:0 0 244px;border-right:1px solid var(--line);display:flex;flex-direction:column;min-height:0;}
.pane{flex:1;display:flex;flex-direction:column;min-width:0;background:var(--surface);}
.tabs{flex:0 0 34px;display:flex;align-items:stretch;border-bottom:1px solid var(--line);background:var(--bg);}
.tab{display:flex;align-items:center;gap:7px;padding:0 13px;font-size:12px;color:var(--dim);
 border-right:1px solid var(--line);cursor:default;}
.tab.on{background:var(--surface);color:var(--text);box-shadow:inset 0 -1px 0 var(--surface);font-weight:500;}
.tool{flex:0 0 36px;display:flex;align-items:center;gap:8px;padding:0 10px;
 border-bottom:1px solid var(--line);background:var(--surface);}
.inp{display:flex;align-items:center;gap:6px;height:23px;padding:0 8px;border:1px solid var(--line);
 border-radius:4px;background:var(--bg);color:var(--faint);font-size:11px;}
.btn{display:flex;align-items:center;gap:6px;height:23px;padding:0 9px;border:1px solid var(--line);
 border-radius:4px;background:var(--bg);color:var(--dim);font-size:11px;font-weight:500;}
.btn.pri{background:var(--accent);border-color:var(--accent);color:#fff;}
.btn.dgr{background:var(--danger);border-color:var(--danger);color:#fff;}
.kbd{font-family:'IBM Plex Mono',ui-monospace,Menlo,monospace;font-size:10px;color:var(--faint);
 border:1px solid var(--hair);border-radius:4px;padding:1px 4px;background:var(--bg);}
.ghead{display:grid;flex:0 0 28px;background:var(--alt);border-bottom:1px solid var(--line);}
.ghead>div{display:flex;align-items:center;gap:5px;padding:0 10px;font-size:11px;font-weight:500;
 color:var(--dim);border-right:1px solid var(--hair);letter-spacing:.02em;overflow:hidden;}
.grow{display:grid;flex:0 0 26px;border-bottom:1px solid var(--hair);}
.grow>div{display:flex;align-items:center;padding:0 10px;font-size:12px;border-right:1px solid var(--hair);
 overflow:hidden;white-space:nowrap;text-overflow:ellipsis;}
.grow.sel{background:var(--sel);}
.num{justify-content:flex-end;color:var(--dim);}
.null{color:var(--dim);font-style:italic;}
.stat{flex:0 0 26px;display:flex;align-items:center;gap:14px;padding:0 12px;font-size:11px;
 color:var(--dim);border-top:1px solid var(--line);background:var(--bg);}
.sect{padding:9px 12px 5px;font-size:10px;font-weight:600;letter-spacing:.07em;
 text-transform:uppercase;color:var(--faint);}
.row{display:flex;align-items:center;gap:7px;height:24px;padding:0 10px;color:var(--text);}
.row.on{background:var(--sel);font-weight:500;}
.dot{width:7px;height:7px;border-radius:50%;flex:0 0 7px;}
.sp{flex:1;}
"""

CHEV_R = ('<svg viewBox="0 0 16 16" width="10" height="10" style="flex:0 0 10px;opacity:.55">'
          '<path d="M6 4l4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.6" '
          'stroke-linecap="round" stroke-linejoin="round"/></svg>')
CHEV_D = ('<svg viewBox="0 0 16 16" width="10" height="10" style="flex:0 0 10px;opacity:.55">'
          '<path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" '
          'stroke-linecap="round" stroke-linejoin="round"/></svg>')
TABLE = ('<svg viewBox="0 0 16 16" width="12" height="12" style="flex:0 0 12px;opacity:.6">'
         '<rect x="2.5" y="3.5" width="11" height="9" rx="1.2" fill="none" stroke="currentColor" '
         'stroke-width="1.2"/><path d="M2.5 6.6h11M6.6 6.6v5.9" stroke="currentColor" '
         'stroke-width="1.2" fill="none"/></svg>')
SEARCH = ('<svg viewBox="0 0 16 16" width="11" height="11" style="flex:0 0 11px;opacity:.55">'
          '<circle cx="7" cy="7" r="4.2" fill="none" stroke="currentColor" stroke-width="1.4"/>'
          '<path d="M10.2 10.2L13.5 13.5" stroke="currentColor" stroke-width="1.4" '
          'stroke-linecap="round"/></svg>')
LOCK = ('<svg viewBox="0 0 16 16" width="11" height="11" style="flex:0 0 11px;opacity:.75">'
        '<rect x="3.5" y="7" width="9" height="6" rx="1.2" fill="none" stroke="currentColor" '
        'stroke-width="1.3"/><path d="M5.6 7V5.4a2.4 2.4 0 0 1 4.8 0V7" fill="none" '
        'stroke="currentColor" stroke-width="1.3"/></svg>')
STOP = ('<svg viewBox="0 0 16 16" width="10" height="10" style="flex:0 0 10px">'
        '<rect x="4" y="4" width="8" height="8" rx="1.4" fill="currentColor"/></svg>')
PLAY = ('<svg viewBox="0 0 16 16" width="10" height="10" style="flex:0 0 10px">'
        '<path d="M5 3.6l7 4.4-7 4.4z" fill="currentColor"/></svg>')
WARN = ('<svg viewBox="0 0 16 16" width="13" height="13" style="flex:0 0 13px">'
        '<path d="M8 2.6l6 10.8H2z" fill="none" stroke="currentColor" stroke-width="1.3" '
        'stroke-linejoin="round"/><path d="M8 6.6v3.1M8 11.5v.1" stroke="currentColor" '
        'stroke-width="1.3" stroke-linecap="round"/></svg>')


def page(title, tokens, w, h, inner, extra=""):
    return f"""<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <script src="./support.js"></script>
</head>
<body>
<x-dc>
<helmet>
  {FONTS}
  <style>
:root{{{tokens}}}
{BASE}
.app{{width:{w}px;height:{h}px;}}
{extra}
  </style>
</helmet>
{inner}
</x-dc>
</body>
</html>
"""


# ---------------------------------------------------------------- shared bits

def titlebar(name, engine, colour, extra_right=""):
    return f"""<div class="tbar">
  <div class="lights"><span class="lt" style="background:#e05c4b"></span>
   <span class="lt" style="background:#dfa63a"></span>
   <span class="lt" style="background:#4fa864"></span></div>
  <div style="display:flex;align-items:center;gap:7px;height:23px;padding:0 10px;
   border:1px solid var(--line);border-radius:11px;background:var(--surface)">
    <span class="dot" style="background:{colour}"></span>
    <span style="font-weight:500">{name}</span>
    <span style="color:var(--faint)">{engine}</span>
  </div>
  <div class="sp"></div>{extra_right}
  <div class="inp" style="width:210px">{SEARCH}<span>Search tables, columns</span>
    <span class="sp"></span><span class="kbd">&#8984;K</span></div>
</div>"""


CONNS = [
    ("local", "#3d7d55", "mysql 8.0", True),
    ("staging", "#a8792c", "mariadb 11", False),
    ("prod", "#9e4436", "mysql 8.0", False),
]
CONNS_DARK = [
    ("local", "#6aab80", "mysql 8.0", True),
    ("staging", "#c99a4e", "mariadb 11", False),
    ("prod", "#c9705d", "mysql 8.0", False),
]


def sidebar(active_table="users", conns=CONNS):
    conn_rows = []
    for nm, col, eng, open_ in conns:
        chev = CHEV_D if open_ else CHEV_R
        lock = f'<span style="color:var(--danger)">{LOCK}</span>' if nm == "prod" else ""
        conn_rows.append(
            f'<div class="row">{chev}<span class="dot" style="background:{col}"></span>'
            f'<span style="font-weight:500">{nm}</span>'
            f'<span style="color:var(--faint);font-size:11px">{eng}</span>'
            f'<span class="sp"></span>{lock}</div>')

    tables = ["order_items", "orders", "payments", "products", "sessions",
              "shipping_zones", "user_roles", "users"]
    tbl_rows = []
    for t in tables:
        on = " on" if t == active_table else ""
        tbl_rows.append(
            f'<div class="row{on}" style="padding-left:44px">{TABLE}'
            f'<span>{t}</span></div>')

    return f"""<div class="side">
  <div class="sect">Connections</div>
  {''.join(conn_rows)}
  <div class="sect" style="margin-top:4px">shopdb</div>
  <div class="row" style="padding-left:22px">{CHEV_D}<span style="font-weight:500">Tables</span>
    <span style="color:var(--faint);font-size:11px">8</span></div>
  {''.join(tbl_rows)}
  <div class="row" style="padding-left:22px">{CHEV_R}<span style="font-weight:500">Views</span>
    <span style="color:var(--faint);font-size:11px">3</span></div>
  <div class="row" style="padding-left:22px">{CHEV_R}<span style="font-weight:500">Routines</span>
    <span style="color:var(--faint);font-size:11px">2</span></div>
  <div class="sp"></div>
  <div style="padding:8px 12px;border-top:1px solid var(--line);display:flex;
   align-items:center;gap:6px;color:var(--faint);font-size:11px">
    <span>Introspected 2m ago</span><span class="sp"></span><span class="kbd">&#8984;R</span></div>
</div>"""


COLS = "70px 236px 186px 104px 158px 158px 84px 1fr"
HEADERS = [("id", "num"), ("email", ""), ("full_name", ""), ("role", ""),
           ("created_at", ""), ("last_login_at", ""), ("is_active", ""), ("", "")]

USERS = [
    (1041, "ama.osei@example.com", "Ama Osei", "admin", "2024-01-14 09:02", "2026-09-09 21:14", "1"),
    (1042, "j.lindqvist@example.com", "Johan Lindqvist", "member", "2024-01-19 16:40", "2026-09-08 11:52", "1"),
    (1043, "priya.raman@example.com", "Priya Raman", "member", "2024-02-02 08:15", "2026-09-10 07:31", "1"),
    (1044, "t.okafor@example.com", "Tobenna Okafor", "support", "2024-02-11 13:27", "2026-08-30 19:05", "1"),
    (1045, "m.delacroix@example.com", "Margot Delacroix", "member", "2024-02-28 10:51", None, "0"),
    (1046, "s.haddad@example.com", "Samir Haddad", "member", "2024-03-05 17:33", "2026-09-07 14:22", "1"),
    (1047, "k.nakamura@example.com", "Kaori Nakamura", "admin", "2024-03-18 07:09", "2026-09-10 06:48", "1"),
    (1048, "e.varga@example.com", "Eszter Varga", "member", "2024-04-01 12:44", "2026-06-11 09:17", "0"),
    (1049, "d.mwangi@example.com", "Deshan Mwangi", "support", "2024-04-22 15:12", "2026-09-09 18:03", "1"),
    (1050, "l.fontaine@example.com", "Luc Fontaine", "member", "2024-05-07 09:38", "2026-09-05 20:41", "1"),
    (1051, "r.abramov@example.com", "Rina Abramov", "member", "2024-05-19 11:20", None, "0"),
    (1052, "b.oyelaran@example.com", "Bisi Oyelaran", "member", "2024-06-02 14:55", "2026-09-10 05:26", "1"),
    (1053, "n.svensson@example.com", "Nils Svensson", "admin", "2024-06-14 08:31", "2026-09-06 16:09", "1"),
    (1054, "c.moretti@example.com", "Chiara Moretti", "member", "2024-06-29 19:06", "2026-09-02 13:38", "1"),
    (1055, "y.tanaka@example.com", "Yuki Tanaka", "support", "2024-07-11 10:14", "2026-09-08 08:55", "1"),
    (1056, "f.almeida@example.com", "Filipa Almeida", "member", "2024-07-26 16:49", "2026-07-19 22:10", "0"),
    (1057, "g.petrov@example.com", "Georgi Petrov", "member", "2024-08-08 07:57", "2026-09-09 12:04", "1"),
    (1058, "h.saito@example.com", "Haruki Saito", "member", "2024-08-21 13:03", "2026-09-10 04:19", "1"),
    (1059, "i.kowalski@example.com", "Iwona Kowalski", "support", "2024-09-03 09:26", "2026-09-01 17:47", "1"),
    (1060, "o.bekele@example.com", "Oda Bekele", "member", "2024-09-17 11:41", None, "0"),
    (1061, "v.ortega@example.com", "Valeria Ortega", "member", "2024-10-01 15:18", "2026-09-07 10:32", "1"),
    (1062, "z.mensah@example.com", "Zuri Mensah", "member", "2024-10-14 08:52", "2026-09-10 03:07", "1"),
    (1063, "a.lindgren@example.com", "Anneli Lindgren", "admin", "2024-10-28 12:35", "2026-09-08 15:20", "1"),
]


def grid_header():
    cells = []
    for name, cls in HEADERS:
        sort = ""
        if name == "id":
            sort = f'<span style="color:var(--accent);font-size:9px">&#9650;</span>'
        cells.append(f'<div class="{cls}">{sort}<span>{name}</span></div>')
    return f'<div class="ghead" style="grid-template-columns:{COLS}">{"".join(cells)}</div>'


def cell(v, cls=""):
    if v is None:
        return f'<div class="{cls}"><span class="null">NULL</span></div>'
    return f'<div class="{cls}">{v}</div>'


def user_row(u, sel=False, style="", cell_over=None):
    over = cell_over or {}
    cls = "grow sel" if sel else "grow"
    cells = [
        over.get(0, f'<div class="num m">{u[0]}</div>'),
        over.get(1, f'<div class="m">{u[1]}</div>'),
        over.get(2, f'<div>{u[2]}</div>'),
        over.get(3, f'<div class="m">{u[3]}</div>'),
        over.get(4, f'<div class="m" style="color:var(--dim)">{u[4]}</div>'),
        over.get(5, cell(f'<span class="m" style="color:var(--dim)">{u[5]}</span>'
                         if u[5] else None)),
        over.get(6, f'<div class="m">{u[6]}</div>'),
        over.get(7, '<div></div>'),
    ]
    return (f'<div class="{cls}" style="grid-template-columns:{COLS};{style}">'
            f'{"".join(cells)}</div>')


# ------------------------------------------------------------------- artboards

def build_main():
    rows = "".join(user_row(u, sel=(u[0] == 1047)) for u in USERS)
    inner = f"""<div class="app">
  {titlebar("local", "shopdb &middot; mysql", "#3d7d55")}
  <div class="body">
    {sidebar()}
    <div class="pane">
      <div class="tabs">
        <div class="tab on">{TABLE}<span>users</span></div>
        <div class="tab">{TABLE}<span>orders</span></div>
        <div class="tab"><span>recent_sessions.sql</span></div>
        <div class="tab" style="color:var(--faint)">+</div>
      </div>
      <div class="tool">
        <div class="inp" style="width:230px">{SEARCH}<span>Filter rows</span>
          <span class="sp"></span><span class="kbd">&#8984;F</span></div>
        <div class="btn"><span>id</span>
          <span style="color:var(--accent);font-size:9px">&#9650;</span></div>
        <div class="btn">WHERE</div>
        <div class="sp"></div>
        <div class="btn">{PLAY}<span>SQL Editor</span><span class="kbd">&#8984;E</span></div>
      </div>
      {grid_header()}
      <div style="flex:1;overflow:hidden;display:flex;flex-direction:column">{rows}</div>
    </div>
  </div>
  <div class="stat">
    <span><b style="font-weight:600">1,284</b> rows</span>
    <span>500 fetched &middot; keyset</span>
    <span>12 ms</span>
    <span class="sp"></span>
    <span class="m">utf8mb4_0900_ai_ci</span><span class="m">InnoDB</span>
  </div>
</div>"""
    return page("Main window", LIGHT, 1440, 760, inner)


def build_staged():
    def mod(new, old, mono=True):
        m = " m" if mono else ""
        return (f'<div class="{m.strip()}" style="background:var(--warn-bg);'
                f'box-shadow:inset 2px 0 0 var(--warn);gap:7px">'
                f'<span>{new}</span>'
                f'<span style="color:var(--faint);text-decoration:line-through">'
                f'{old}</span></div>')

    out = []
    for u in USERS[:20]:
        if u[0] == 1043:
            out.append(user_row(u, cell_over={3: mod("admin", "member")}))
        elif u[0] == 1045:
            out.append(user_row(u, cell_over={6: mod("1", "0")}))
        elif u[0] == 1051:
            struck = ("text-decoration:line-through;color:var(--faint);"
                      "background:var(--danger-bg);")
            out.append(user_row(u, style=f"{struck}box-shadow:inset 2px 0 0 var(--danger)"))
        else:
            out.append(user_row(u))

    new_row = (f'<div class="grow" style="grid-template-columns:{COLS};'
               f'background:var(--ok-bg);box-shadow:inset 2px 0 0 var(--ok)">'
               f'<div class="num m" style="color:var(--ok)">AUTO</div>'
               f'<div class="m">n.adeyemi@example.com</div>'
               f'<div>Nkechi Adeyemi</div>'
               f'<div class="m">support</div>'
               f'<div class="m" style="color:var(--faint)">DEFAULT</div>'
               f'<div><span class="null">NULL</span></div>'
               f'<div class="m">1</div><div></div></div>')
    out.append(new_row)

    commit = """<div style="flex:0 0 42px;display:flex;align-items:center;gap:10px;padding:0 12px;
   border-top:1px solid var(--line);background:var(--alt)">
    <span style="display:flex;align-items:center;gap:6px">
      <span class="dot" style="background:var(--warn)"></span>
      <b style="font-weight:600">4 changes</b>
      <span style="color:var(--dim)">2 updates &middot; 1 insert &middot; 1 delete</span></span>
    <span class="sp"></span>
    <div class="btn">Preview SQL<span class="kbd">&#8984;&#8679;P</span></div>
    <div class="btn">Discard</div>
    <div class="btn pri">Commit<span class="kbd" style="color:#fff;border-color:#ffffff55">&#8984;S</span></div>
  </div>"""

    inner = f"""<div class="app">
  {titlebar("local", "shopdb &middot; mysql", "#6aab80")}
  <div class="body">
    {sidebar(conns=CONNS_DARK)}
    <div class="pane">
      <div class="tabs">
        <div class="tab on">{TABLE}<span>users</span>
          <span class="dot" style="background:var(--warn)"></span></div>
        <div class="tab">{TABLE}<span>orders</span></div>
        <div class="tab"><span>recent_sessions.sql</span></div>
        <div class="tab" style="color:var(--faint)">+</div>
      </div>
      <div class="tool">
        <div class="inp" style="width:230px">{SEARCH}<span>Filter rows</span>
          <span class="sp"></span><span class="kbd">&#8984;F</span></div>
        <div class="btn">WHERE</div>
        <div class="sp"></div>
        <div class="btn">+ Row<span class="kbd">&#8984;N</span></div>
        <div class="btn">Delete Row<span class="kbd">&#8984;&#8998;</span></div>
      </div>
      {grid_header()}
      <div style="flex:1;overflow:hidden;display:flex;flex-direction:column">{''.join(out)}</div>
      {commit}
    </div>
  </div>
  <div class="stat">
    <span>Nothing written until you commit</span>
    <span class="sp"></span>
    <span>Transaction &middot; optimistic concurrency on <span class="m">id</span></span>
  </div>
</div>"""
    dark_btn = (".btn.pri{color:#1b1a18;}"
                ".btn.pri .kbd{color:#1b1a18;border-color:#1b1a1855;}"
                ".btn.dgr{color:#1b1a18;}")
    return page("Staged edits", DARK, 1440, 760, inner, extra=dark_btn)


SQL_CSS = """
.sql{padding:12px 0;font-family:'IBM Plex Mono',ui-monospace,Menlo,monospace;font-size:12px;
 line-height:21px;display:flex;}
.gut{flex:0 0 46px;text-align:right;padding-right:12px;color:var(--faint);
 user-select:none;white-space:pre;}
.code{flex:1;white-space:pre;}
.k{color:var(--accent);font-weight:500;} .s{color:var(--warn);} .n{color:var(--ok);}
.c{color:var(--faint);font-style:italic;} .f{color:var(--text);font-weight:500;}
.o{color:var(--dim);}
.caret{display:inline-block;width:1.5px;height:15px;background:var(--accent);
 vertical-align:-3px;}
"""


def build_query():
    code = (
        '<span class="c">-- Sessions for active users, most recent first</span>\n'
        '<span class="k">SELECT</span> s.id<span class="o">,</span>\n'
        '       u.email<span class="o">,</span>\n'
        '       u.role<span class="o">,</span>\n'
        '       s.started_at<span class="o">,</span>\n'
        '       <span class="f">ROUND</span><span class="o">(</span>s.duration_ms '
        '<span class="o">/</span> <span class="n">1000</span><span class="o">,</span> '
        '<span class="n">1</span><span class="o">)</span> <span class="k">AS</span> seconds<span class="o">,</span>\n'
        '       s.ip_address\n'
        '<span class="k">FROM</span> sessions <span class="k">AS</span> s\n'
        '<span class="k">JOIN</span> users    <span class="k">AS</span> u '
        '<span class="k">ON</span> u.id <span class="o">=</span> s.user_id\n'
        '<span class="k">WHERE</span> s.started_at <span class="o">&gt;=</span> '
        '<span class="f">NOW</span><span class="o">()</span> <span class="o">-</span> '
        '<span class="k">INTERVAL</span> <span class="n">12</span> <span class="k">WEEK</span>\n'
        '  <span class="k">AND</span> u.is_active <span class="o">=</span> <span class="n">1</span>\n'
        '<span class="k">ORDER BY</span> s.started_at <span class="k">DESC</span>'
        '<span class="o">;</span><span class="caret"></span>'
    )
    lines = "".join(f"{i}\n" for i in range(1, 13))

    rcols = "92px 236px 104px 158px 96px 132px 1fr"
    res = [
        ("884213", "h.saito@example.com", "member", "2026-09-10 04:19", "612.4", "10.14.2.51"),
        ("884212", "b.oyelaran@example.com", "member", "2026-09-10 05:26", "188.0", "10.14.7.9"),
        ("884211", "k.nakamura@example.com", "admin", "2026-09-10 06:48", "1204.8", "10.14.2.18"),
        ("884210", "priya.raman@example.com", "member", "2026-09-10 07:31", "455.2", "10.22.4.77"),
        ("884209", "ama.osei@example.com", "admin", "2026-09-09 21:14", "980.6", "10.14.2.31"),
        ("884208", "d.mwangi@example.com", "support", "2026-09-09 18:03", "731.9", "10.22.4.12"),
        ("884207", "g.petrov@example.com", "member", "2026-09-09 12:04", "302.5", "10.14.9.64"),
        ("884206", "j.lindqvist@example.com", "member", "2026-09-08 11:52", "144.7", "10.22.1.40"),
        ("884205", "y.tanaka@example.com", "support", "2026-09-08 08:55", "889.1", "10.14.7.23"),
        ("884204", "n.svensson@example.com", "admin", "2026-09-06 16:09", "1055.3", "10.14.2.44"),
    ]
    rrows = "".join(
        f'<div class="grow" style="grid-template-columns:{rcols}">'
        f'<div class="num m">{a}</div><div class="m">{b}</div><div class="m">{c}</div>'
        f'<div class="m" style="color:var(--dim)">{d}</div>'
        f'<div class="num m">{e}</div><div class="m">{f}</div><div></div></div>'
        for a, b, c, d, e, f in res)
    rhead = (f'<div class="ghead" style="grid-template-columns:{rcols}">'
             f'<div class="num">id</div><div>email</div><div>role</div>'
             f'<div>started_at</div><div class="num">seconds</div>'
             f'<div>ip_address</div><div></div></div>')

    runbar = f"""<div style="flex:0 0 34px;display:flex;align-items:center;gap:10px;padding:0 12px;
   border-top:1px solid var(--line);border-bottom:1px solid var(--line);background:var(--alt)">
    <span style="display:flex;align-items:center;gap:7px;color:var(--accent);font-weight:500">
      <span style="width:8px;height:8px;border-radius:50%;background:var(--accent);
       box-shadow:0 0 0 3px var(--accent-bg)"></span>Running</span>
    <span class="m" style="color:var(--dim)">4.2 s</span>
    <div style="width:180px;height:3px;border-radius:2px;background:var(--hair);overflow:hidden">
      <div style="width:62%;height:100%;background:var(--accent)"></div></div>
    <span style="color:var(--dim)"><b class="m" style="color:var(--text)">12,480</b> rows streamed</span>
    <span class="sp"></span>
    <div class="btn dgr">{STOP}<span>Cancel</span>
      <span class="kbd" style="color:#fff;border-color:#ffffff55">&#8984;.</span></div>
  </div>"""

    inner = f"""<div class="app">
  {titlebar("local", "shopdb &middot; mysql", "#3d7d55")}
  <div class="body">
    {sidebar(active_table=None)}
    <div class="pane">
      <div class="tabs">
        <div class="tab">{TABLE}<span>users</span></div>
        <div class="tab">{TABLE}<span>orders</span></div>
        <div class="tab on"><span>recent_sessions.sql</span></div>
        <div class="tab" style="color:var(--faint)">+</div>
      </div>
      <div class="tool">
        <div class="btn pri">{PLAY}<span>Run</span>
          <span class="kbd" style="color:#fff;border-color:#ffffff55">&#8984;R</span></div>
        <div class="btn">Explain</div>
        <div class="btn">Format<span class="kbd">&#8984;&#8679;F</span></div>
        <div class="sp"></div>
        <div class="btn">History</div>
        <div class="btn">Export CSV</div>
      </div>
      <div class="sql" style="flex:0 0 276px;background:var(--surface);overflow:hidden">
        <div class="gut">{lines}</div><div class="code">{code}</div>
      </div>
      {runbar}
      {rhead}
      <div style="flex:1;overflow:hidden;display:flex;flex-direction:column">{rrows}</div>
    </div>
  </div>
  <div class="stat">
    <span>Streaming &mdash; results appear as they arrive</span>
    <span class="sp"></span>
    <span>Cancel stays reachable at all times</span>
  </div>
</div>"""
    return page("SQL editor", LIGHT, 1440, 760, inner, extra=SQL_CSS)


DLG_CSS = """
.fld{display:flex;flex-direction:column;gap:5px;}
.lbl{font-size:11px;color:var(--dim);font-weight:500;letter-spacing:.01em;}
.txt{height:28px;border:1px solid var(--line);border-radius:5px;background:var(--surface);
 display:flex;align-items:center;padding:0 9px;font-size:12px;}
.txt.ph{color:var(--faint);}
.txt.foc{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-bg);}
.seg{display:flex;border:1px solid var(--line);border-radius:5px;overflow:hidden;}
.seg>div{flex:1;height:28px;display:flex;align-items:center;justify-content:center;
 font-size:12px;border-right:1px solid var(--line);background:var(--surface);}
.seg>div:last-child{border-right:0;}
.seg>div.on{background:var(--accent);color:#fff;font-weight:500;}
.sw{width:26px;height:26px;border-radius:50%;border:2px solid transparent;}
.sw.on{border-color:var(--text);box-shadow:inset 0 0 0 2px var(--surface);}
.chk{width:14px;height:14px;border-radius:3.5px;border:1.5px solid var(--line);
 background:var(--surface);flex:0 0 14px;}
.chk.on{background:var(--danger);border-color:var(--danger);}
.crow{display:flex;align-items:center;gap:8px;font-size:12px;}
.tgl{width:30px;height:17px;border-radius:9px;background:var(--danger);
 display:flex;align-items:center;justify-content:flex-end;padding:2px;flex:0 0 30px;}
.tgl>span{width:13px;height:13px;border-radius:50%;background:#fff;display:block;}
.caret{display:inline-block;width:1.5px;height:14px;background:var(--accent);
 vertical-align:-3px;margin-left:1px;}
.app .btn{height:28px;padding:0 13px;}
.chk{border-radius:4px;}
"""


def build_dialog():
    swatches = ["#3d7d55", "#24707a", "#4a6ea8", "#7a5aa0", "#a8792c", "#9e4436"]
    sw = "".join(
        f'<span class="sw{" on" if c == "#9e4436" else ""}" style="background:{c}"></span>'
        for c in swatches)

    inner = f"""<div class="app" style="background:var(--bg)">
  <div style="flex:0 0 46px;display:flex;align-items:center;padding:0 18px;
   border-bottom:1px solid var(--line)">
    <b style="font-size:14px;font-weight:600">New Connection</b>
    <span class="sp"></span><span style="color:var(--faint)">&#10005;</span>
  </div>

  <div style="flex:1;padding:18px;display:flex;flex-direction:column;gap:15px;overflow:hidden">

    <div class="fld"><span class="lbl">Driver</span>
      <div class="seg"><div class="on">MySQL</div><div>MariaDB</div><div>SQLite</div></div></div>

    <div class="fld"><span class="lbl">Name</span>
      <div class="txt foc">prod<span class="caret"></span></div></div>

    <div style="display:grid;grid-template-columns:1fr 96px;gap:12px">
      <div class="fld"><span class="lbl">Host</span>
        <div class="txt m">db-primary.internal</div></div>
      <div class="fld"><span class="lbl">Port</span>
        <div class="txt m">3306</div></div>
    </div>

    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px">
      <div class="fld"><span class="lbl">Username</span>
        <div class="txt m">app_readonly</div></div>
      <div class="fld"><span class="lbl">Password</span>
        <div class="txt m">&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;&bull;</div></div>
    </div>
    <div style="margin-top:-9px;display:flex;align-items:center;gap:6px;
     font-size:11px;color:var(--dim)">{LOCK}<span>Stored in the system keychain, never in the app's config file</span></div>

    <div class="fld"><span class="lbl">Database</span>
      <div class="txt ph">All databases</div></div>

    <div class="fld"><span class="lbl">Colour</span>
      <div style="display:flex;gap:9px;align-items:center">{sw}
        <span class="sp"></span>
        <span style="font-size:11px;color:var(--dim)">Tints the window and every tab</span></div></div>

    <div style="border:1px solid var(--danger);border-radius:7px;background:var(--danger-bg);
     padding:12px 13px;display:flex;flex-direction:column;gap:10px">
      <div style="display:flex;align-items:center;gap:8px;color:var(--danger)">
        {WARN}<b style="font-weight:600;font-size:12px">Production connection</b>
        <span class="sp"></span><span class="tgl"><span></span></span></div>
      <div class="crow"><span class="chk on" style="display:flex;align-items:center;
       justify-content:center;color:#fff;font-size:10px">&#10003;</span>
        <span>Open new tabs in read-only mode</span></div>
      <div class="crow"><span class="chk on" style="display:flex;align-items:center;
       justify-content:center;color:#fff;font-size:10px">&#10003;</span>
        <span>Confirm before <span class="m">UPDATE</span> or <span class="m">DELETE</span>
        without <span class="m">WHERE</span></span></div>
    </div>
  </div>

  <div style="flex:0 0 58px;display:flex;align-items:center;gap:9px;padding:0 18px;
   border-top:1px solid var(--line)">
    <div class="btn">Test Connection</div>
    <span style="display:flex;align-items:center;gap:6px;font-size:11px;color:var(--ok)">
      <span class="dot" style="background:var(--ok)"></span>Reachable &middot; 24 ms</span>
    <span class="sp"></span>
    <div class="btn">Cancel</div>
    <div class="btn dgr">Connect
      <span class="kbd" style="color:#fff;border-color:#ffffff55">&#8984;&#9166;</span></div>
  </div>
</div>"""
    return page("Connection dialog", LIGHT, 620, 720, inner, extra=DLG_CSS)


STATE_CSS = """
.wrap{width:1240px;height:720px;padding:26px;display:grid;
 grid-template-columns:repeat(2,minmax(0,1fr));grid-template-rows:repeat(2,minmax(0,1fr));
 gap:26px;background:var(--bg);}
.panel{background:var(--surface);border:1px solid var(--line);border-radius:9px;
 display:flex;flex-direction:column;overflow:hidden;}
.ptitle{flex:0 0 31px;border-bottom:1px solid var(--hair);display:flex;align-items:center;
 padding:0 13px;font-size:10px;font-weight:600;letter-spacing:.08em;text-transform:uppercase;
 color:var(--faint);}
.pbody{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;
 gap:11px;padding:22px;text-align:center;}
.bar{width:230px;height:4px;border-radius:2px;background:var(--hair);overflow:hidden;}
.bar>div{height:100%;background:var(--accent);}
.badge{font-size:10px;font-weight:600;letter-spacing:.07em;padding:3px 7px;border-radius:4px;}
.big{font-size:14px;font-weight:600;}
.sub{font-size:12px;color:var(--dim);max-width:340px;line-height:1.5;}
"""


def build_states():
    inner = f"""<div class="wrap">

  <div class="panel"><div class="ptitle">Connecting</div>
    <div class="pbody">
      <span class="dot" style="width:11px;height:11px;background:var(--danger);
       box-shadow:0 0 0 5px var(--danger-bg)"></span>
      <span class="big">Connecting to prod</span>
      <span class="sub m" style="font-size:11px">db-primary.internal:3306</span>
      <div class="btn" style="margin-top:4px">Cancel<span class="kbd">esc</span></div>
    </div></div>

  <div class="panel"><div class="ptitle">Introspecting a large schema</div>
    <div class="pbody">
      <span class="big">Reading schema</span>
      <div class="bar"><div style="width:32%"></div></div>
      <span class="sub"><b class="m" style="font-weight:500">412</b> of
        <b class="m" style="font-weight:500">906</b> tables</span>
      <span class="sub" style="font-size:11px">Tables appear in the sidebar as they load &mdash;
        columns are read only when you expand one.</span>
    </div></div>

  <div class="panel"><div class="ptitle">Empty result</div>
    <div class="pbody">
      <span style="opacity:.45;transform:scale(2.1);margin-bottom:6px">{TABLE}</span>
      <span class="big">No rows returned</span>
      <span class="sub m" style="font-size:11px;background:var(--alt);padding:7px 10px;
       border-radius:5px;border:1px solid var(--hair)">
        SELECT * FROM users WHERE role = 'owner'</span>
      <span class="sub" style="font-size:11px">The query ran in 8&nbsp;ms.
        <a href="#">Clear the filter</a> to see all 1,284 rows.</span>
    </div></div>

  <div class="panel" style="border-color:var(--danger)">
    <div class="ptitle" style="color:var(--danger)">Error</div>
    <div class="pbody">
      <span class="badge" style="background:var(--danger-bg);color:var(--danger)">AUTH</span>
      <span class="big">Access denied for user &lsquo;app_readonly&rsquo;</span>
      <span class="sub">The credentials in your keychain were rejected by
        <span class="m">db-primary.internal</span>. Nothing was run.</span>
      <span class="sub m" style="font-size:11px;color:var(--dim);background:var(--alt);
       padding:6px 9px;border-radius:5px;border:1px solid var(--hair)">
        ERROR 1045 (28000): Access denied &middot; using password: YES</span>
      <div style="display:flex;gap:8px;margin-top:2px">
        <div class="btn">Edit connection</div>
        <div class="btn pri">Retry<span class="kbd" style="color:#fff;
         border-color:#ffffff55">&#8984;R</span></div></div>
    </div></div>

</div>"""
    return page("States", LIGHT, 1240, 720, inner, extra=STATE_CSS)


FND_CSS = """
.fwrap{width:940px;height:1120px;padding:30px 34px;display:flex;flex-direction:column;
 gap:22px;background:var(--bg);overflow:hidden;}
.h2{font-size:10px;font-weight:600;letter-spacing:.1em;text-transform:uppercase;
 color:var(--faint);padding-bottom:7px;border-bottom:1px solid var(--line);}
.card{background:var(--surface);border:1px solid var(--line);border-radius:8px;padding:15px 17px;}
.swz{display:flex;flex-direction:column;gap:5px;align-items:flex-start;}
.chip{width:100%;height:34px;border-radius:5px;border:1px solid rgba(0,0,0,.09);}
.cname{font-size:10px;color:var(--dim);} .chex{font-size:10px;color:var(--faint);}
.krow{display:flex;align-items:center;gap:9px;height:21px;font-size:11px;}
.kk{font-family:'IBM Plex Mono',ui-monospace,Menlo,monospace;font-size:10px;
 border:1px solid var(--line);border-radius:3px;padding:1px 5px;background:var(--bg);
 color:var(--dim);flex:0 0 auto;min-width:34px;text-align:center;}
.dsr{display:flex;align-items:baseline;gap:12px;height:23px;font-size:11px;color:var(--dim);}
.dsr b{font-weight:600;color:var(--text);font-family:'IBM Plex Mono',ui-monospace,monospace;
 font-size:11px;min-width:62px;}
"""

LIGHT_SW = [("bg", "#f2f1ee"), ("surface", "#fbfaf8"), ("alt", "#f6f5f2"), ("line", "#dedbd5"),
            ("hair", "#eae7e2"), ("text", "#23211e"), ("dim", "#6f6a63"), ("faint", "#7d766d"),
            ("sel", "#e7f0f0"), ("accent", "#24707a"), ("accent-bg", "#deeef0"),
            ("danger", "#9e4436"), ("danger-bg", "#f6e6e2"), ("warn", "#a8792c"),
            ("warn-bg", "#f7eeda"), ("ok", "#3d7d55"), ("ok-bg", "#e3efe6")]
DARK_SW = [("bg", "#1b1a18"), ("surface", "#212020"), ("alt", "#262523"), ("line", "#35322e"),
           ("hair", "#2b2926"), ("text", "#e9e6e0"), ("dim", "#9b948b"), ("faint", "#7e776f"),
           ("sel", "#1e3335"), ("accent", "#57aab2"), ("accent-bg", "#173a3c"),
           ("danger", "#c9705d"), ("danger-bg", "#3a221d"), ("warn", "#c99a4e"),
           ("warn-bg", "#382c17"), ("ok", "#6aab80"), ("ok-bg", "#1e3325")]

KEYS = [
    ("Global", [("&#8984;K", "Quick-open any connection, table or saved query"),
                ("&#8984;T", "New tab"), ("&#8984;W", "Close tab"),
                ("&#8984;1&ndash;9", "Jump to tab"), ("&#8984;E", "Toggle SQL editor"),
                ("&#8984;R", "Refresh table / re-run query")]),
    ("Grid", [("&#8593;&#8595;&#8592;&#8594;", "Move selection"),
              ("&#8677;", "Next cell &middot; &#8679;&#8677; previous"),
              ("&#9166;", "Edit cell &middot; &#9166; again commits to the change set"),
              ("esc", "Abandon the edit"), ("&#8984;N", "Insert row"),
              ("&#8984;&#8998;", "Mark row deleted"), ("&#8984;F", "Filter rows")]),
    ("Editor", [("&#8984;R", "Run"), ("&#8984;&#8679;R", "Run selection"),
                ("&#8984;.", "Cancel the running query"),
                ("&#8984;&#8679;F", "Format SQL"), ("&#8984;/", "Toggle comment")]),
    ("Changes", [("&#8984;S", "Commit staged changes"),
                 ("&#8984;&#8679;P", "Preview generated SQL"),
                 ("&#8984;Z", "Undo the last staged change")]),
]


def build_foundations():
    def swatches(pairs, bg):
        cells = "".join(
            f'<div class="swz"><div class="chip" style="background:{hexv}"></div>'
            f'<span class="cname">{name}</span><span class="chex m">{hexv}</span></div>'
            for name, hexv in pairs)
        return (f'<div style="display:grid;grid-template-columns:repeat(9,minmax(0,1fr));'
                f'gap:9px;background:{bg};padding:13px;border-radius:7px;'
                f'border:1px solid var(--line)">{cells}</div>')

    kcols = []
    for group, items in KEYS:
        rows = "".join(
            f'<div class="krow"><span class="kk">{k}</span>'
            f'<span style="color:var(--dim)">{d}</span></div>' for k, d in items)
        kcols.append(f'<div style="display:flex;flex-direction:column;gap:6px">'
                     f'<span class="cname" style="font-weight:600;color:var(--text)">{group}</span>'
                     f'{rows}</div>')

    density = [("26 px", "Data row height &mdash; the number the whole design is built around"),
               ("28 px", "Column header row"), ("24 px", "Sidebar row"),
               ("34 px", "Tab bar"), ("36 px", "Toolbar"), ("38 px", "Title bar"),
               ("26 px", "Status bar"), ("244 px", "Sidebar width"),
               ("23 px", "Toolbar controls &mdash; buttons, filters, chips"),
               ("28 px", "Form controls in dialogs &mdash; inputs and segmented controls"),
               ("10 px", "Cell gutter, left and right"),
               ("1 px", "Every divider &mdash; hairlines only, no shadows between panes"),
               ("4&ndash;9 px", "Corner radii: controls 4&ndash;5, panels 7&ndash;9. "
                                "The connection pill is the one stadium shape.")]
    drows = "".join(f'<div class="dsr"><b>{a}</b><span>{b}</span></div>' for a, b in density)

    inner = f"""<div class="fwrap">
  <div>
    <div style="font-size:19px;font-weight:600;letter-spacing:-.01em">Foundations</div>
    <div style="font-size:12px;color:var(--dim);margin-top:3px">
      Dense, calm, keyboard-first. Every value here is the one to implement against.</div>
  </div>

  <div>
    <div class="h2">Type</div>
    <div class="card" style="display:flex;flex-direction:column;gap:9px;margin-top:11px">
      <div style="display:flex;align-items:baseline;gap:14px">
        <span style="font-size:19px;font-weight:600">IBM Plex Sans</span>
        <span style="font-size:11px;color:var(--dim)">interface &mdash; 400 / 500 / 600</span></div>
      <div style="display:flex;align-items:baseline;gap:14px">
        <span class="m" style="font-size:19px;font-weight:500">IBM Plex Mono</span>
        <span style="font-size:11px;color:var(--dim)">every value, identifier and SQL token</span></div>
      <div style="height:1px;background:var(--hair);margin:3px 0"></div>
      <div style="display:flex;align-items:baseline;gap:20px;flex-wrap:wrap">
        <span style="font-size:19px;font-weight:600">19 Heading</span>
        <span style="font-size:14px;font-weight:600">14 Subhead</span>
        <span style="font-size:12px">12 Body &amp; data</span>
        <span style="font-size:11px;color:var(--dim)">11 Label</span>
        <span style="font-size:10px;letter-spacing:.1em;text-transform:uppercase;
         color:var(--faint)">10 Section</span></div>
      <div style="font-size:11px;color:var(--dim);margin-top:2px">
        Data cells never fall below 12&nbsp;px. Mono is not decorative &mdash; column alignment
        depends on it.</div>
    </div>
  </div>

  <div>
    <div class="h2">Colour</div>
    <div style="display:flex;flex-direction:column;gap:11px;margin-top:11px">
      {swatches(LIGHT_SW, "#fbfaf8")}
      {swatches(DARK_SW, "#212020")}
    </div>
    <div style="font-size:11px;color:var(--dim);margin-top:9px">
      Accents share one lightness and chroma, varying only hue, so no state shouts louder than
      another. <b style="color:var(--danger);font-weight:600">Danger</b> is reserved for
      production and destructive actions &mdash; never for ordinary errors you can retry.</div>
  </div>

  <div>
    <div class="h2">Density</div>
    <div class="card" style="margin-top:11px">{drows}</div>
  </div>

</div>"""
    kb = f"""<div class="fwrap" style="height:600px">
  <div>
    <div style="font-size:19px;font-weight:600;letter-spacing:-.01em">Keyboard</div>
    <div style="font-size:12px;color:var(--dim);margin-top:3px">
      The app is drivable without a mouse. Every shortcut below is part of the design,
      not a later addition.</div>
  </div>
  <div style="flex:1;display:flex;flex-direction:column;min-height:0">
    <div class="card" style="flex:1;display:grid;
     grid-template-columns:repeat(2,minmax(0,1fr));gap:20px 26px;align-content:start">
      {''.join(kcols)}
    </div>
  </div>
</div>"""
    return (page("Foundations", LIGHT, 940, 1180, inner, extra=FND_CSS),
            page("Keyboard", LIGHT, 940, 600, kb, extra=FND_CSS))


CANVAS = {
    "artboards": [
        {"file": "Main.dc.html", "x": 0, "y": 0, "w": 1440, "h": 760},
        {"file": "StagedEdits.dc.html", "x": 1560, "y": 0, "w": 1440, "h": 760,
         "title": "Staged edits (dark)"},
        {"file": "QueryEditor.dc.html", "x": 3120, "y": 0, "w": 1440, "h": 760},
        {"file": "ConnectionDialog.dc.html", "x": 0, "y": 940, "w": 620, "h": 720},
        {"file": "States.dc.html", "x": 780, "y": 940, "w": 1240, "h": 720},
        {"file": "Foundations.dc.html", "x": 2140, "y": 940, "w": 940, "h": 1180},
        {"file": "Keyboard.dc.html", "x": 3200, "y": 940, "w": 940, "h": 600},
    ],
    "annotations": [
        {"id": "brief", "x": 0, "y": -190, "w": 620,
         "text": "tablepluslike — UI/UX direction\n\nDense, calm, keyboard-first. Original "
                 "visual language, not a copy of any shipping client.\n\nRow 1: the three screens "
                 "you live in. Row 2: entry points, states, and the tokens to build against."},
        {"id": "note-staged", "x": 1560, "y": -120, "w": 560,
         "text": "Nothing reaches the database until Commit. Amber = modified (old value struck "
                 "through beside the new one), green = inserted, red strike = deleted. The tab "
                 "carries a dot so you cannot lose track of a dirty table."},
        {"id": "note-query", "x": 3120, "y": -120, "w": 520,
         "text": "Results stream in while the query runs, and Cancel is on screen the whole time "
                 "— never buried in a menu."},
        {"id": "note-safety", "x": 0, "y": 1710, "w": 620,
         "text": "Connection colour is a safety feature, not decoration. Production is red, tints "
                 "the whole window, and defaults to read-only."},
    ],
    "launch": {"view": "canvas"},
}


def main():
    files = {
        "Main.dc.html": build_main(),
        "StagedEdits.dc.html": build_staged(),
        "QueryEditor.dc.html": build_query(),
        "ConnectionDialog.dc.html": build_dialog(),
        "States.dc.html": build_states(),
    }
    files["Foundations.dc.html"], files["Keyboard.dc.html"] = build_foundations()
    for name, src in files.items():
        (OUT / name).write_text(src, encoding="utf-8")
        print(f"{name}: {len(src):,} bytes")
    (OUT / "canvas.json").write_text(json.dumps(CANVAS, indent=2), encoding="utf-8")
    print("canvas.json written")


if __name__ == "__main__":
    main()
