#!/usr/bin/env python3
# 读取 GNOME/mutter 的当前显示布局，输出 JSON 给 dp-reset-gui 使用。
#
# 数据来源：org.gnome.Mutter.DisplayConfig.GetCurrentState（会话总线，无需 root）
#   - monitors[]：每个 connector 的 vendor/product + 所有可用模式（含 is-current）
#   - logical_monitors[]：逻辑坐标 (x,y)、缩放、旋转、是否主屏，以及该逻辑屏包含哪些 connector
#
# mutter 的返回类型（以 mutter 49+ 为准）：
#   (u  a((ssss)a(siidd a{sv})a{sv})  a(iidub a(ssss) a{sv})  a{sv})
#    serial  显示器           模式       逻辑屏(x,y,scale,transform,primary,mons,props)  全局属性
#
# 关键计算：mutter 的逻辑屏**不直接给宽高**，必须由该屏当前模式的像素尺寸 ÷ 缩放
# 得到；旋转 90°/270° 时宽高互换。
#
# 注意：本脚本只负责「桌面布局」，不判断某个接口能不能重抽 —— 那个判断
# （amdgpu + DP-N + connected）由 Go 侧的 discover.go 从 sysfs 做，避免两处
# 白名单不一致。脚本也不写死任何机器专属接口名/PCI 地址。

import glob
import json
import os
import re
import sys

try:
    from gi.repository import Gio
except Exception as e:  # pragma: no cover
    print(json.dumps({"error": f"cannot import gi: {e}"}))
    sys.exit(1)

SYS_DRM = "/sys/class/drm"
CARD_RE = re.compile(r"^card[0-9]+$")


def _i(v, default=0):
    """转 int，失败回落默认值（D-Bus 值缺失/异常时不让整次读取失败）。"""
    try:
        return int(v)
    except (TypeError, ValueError):
        return default


def _f(v, default=0.0):
    """转 float，失败回落默认值。"""
    try:
        return float(v)
    except (TypeError, ValueError):
        return default


def _read(base, name):
    try:
        with open(os.path.join(base, name)) as f:
            return f.read().strip()
    except OSError:
        return ""


def connector_status(conn):
    """从 sysfs 读 connector 的 status/enabled/dpms 和所属 card（core 侧状态）。"""
    names = [conn]
    # 命名差异：mutter 对外接 HDMI 叫 "HDMI-1"，内核 connector 是 "HDMI-A-1"
    if conn.startswith("HDMI-"):
        names.append("HDMI-A-" + conn[len("HDMI-"):])
    cards = sorted(
        (os.path.basename(p) for p in glob.glob(os.path.join(SYS_DRM, "card*"))),
        key=lambda c: _i(c.replace("card", "")),
    )
    for card in cards:
        if not CARD_RE.match(card):
            continue
        for name in names:
            cand = os.path.join(SYS_DRM, f"{card}-{name}")
            if not os.path.isdir(cand):
                continue
            return {
                "card": card,
                "status": _read(cand, "status"),
                "enabled": _read(cand, "enabled"),
                "dpms": _read(cand, "dpms"),
            }
    return {"card": "", "status": "unknown", "enabled": "", "dpms": ""}


def main():
    bus = Gio.bus_get_sync(Gio.BusType.SESSION, None)
    res = bus.call_sync(
        "org.gnome.Mutter.DisplayConfig",
        "/org/gnome/Mutter/DisplayConfig",
        "org.gnome.Mutter.DisplayConfig",
        "GetCurrentState",
        None,
        None,
        Gio.DBusCallFlags.NONE,
        -1,
        None,
    )
    serial, monitors, logical, props = res.unpack()

    # connector -> 当前模式像素尺寸 / 产品名 / 是否内置
    cur_mode = {}
    product = {}
    builtin = {}
    for m in monitors:
        conn, _vendor, prod, _sid = m[0]
        modes, mprops = m[1], m[2]
        product[conn] = prod.strip()
        builtin[conn] = bool(mprops.get("is-builtin"))
        for mo in modes:
            # 模式结构：(id, w, h, refresh, preferred_scale, supported_scales, props)
            if mo[6].get("is-current"):
                cur_mode[conn] = (mo[0], _i(mo[1]), _i(mo[2]), _f(mo[3]))

    screens = []
    for x, y, scale, transform, primary, mons, _lprops in logical:
        for conn, _vendor, _prod, _sid in mons:
            mode_id, pw, ph, refresh = cur_mode.get(conn, ("", 0, 0, 0.0))
            # 旋转 90°/270°(transform=1/3) 时逻辑宽高互换
            swap = transform in (1, 3)
            lw = (ph if swap else pw) / scale if scale else pw
            lh = (pw if swap else ph) / scale if scale else ph
            st = connector_status(conn)
            screens.append(
                {
                    "connector": conn,
                    "product": product.get(conn, ""),
                    "builtin": builtin.get(conn, False),
                    "x": _i(x),
                    "y": _i(y),
                    "scale": round(_f(scale), 4),
                    "transform": _i(transform),  # 0=正常 1=90° 2=180° 3=270°
                    "primary": bool(primary),
                    "px_w": pw,
                    "px_h": ph,
                    "logical_w": round(_f(lw), 2),
                    "logical_h": round(_f(lh), 2),
                    "mode": mode_id,
                    "refresh": round(refresh, 3),
                    "card": st["card"],
                    "status": st["status"],
                    "enabled": st["enabled"],
                    "dpms": st["dpms"],
                }
            )

    screens.sort(key=lambda s: (s["y"], s["x"]))
    json.dump(
        {
            "serial": _i(serial),
            "layout_mode": _i(props.get("layout-mode", -1), -1),
            "screens": screens,
        },
        sys.stdout,
        ensure_ascii=False,
    )
    sys.stdout.write("\n")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(json.dumps({"error": f"{type(e).__name__}: {e}"}), file=sys.stdout)
        sys.exit(1)
