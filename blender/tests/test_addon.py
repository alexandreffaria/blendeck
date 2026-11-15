"""Runs the add-on's connection code against a fake bpy and real loopback
sockets, so the parts that talk to the bridge are tested without Blender."""

import json
import os
import socket
import sys
import time
import types
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


class _Prefs:
    enabled = True
    host = "127.0.0.1"
    port = 0
    clamp = True


class _Render:
    fps = 24
    fps_base = 1.0


class _Scene:
    name = "Edit"
    frame_current = 10
    frame_start = 1
    frame_end = 100
    render = _Render()

    def frame_set(self, f):
        self.frame_current = f


def _install_fake_bpy():
    bpy = types.ModuleType("bpy")
    prefs = _Prefs()
    scene = _Scene()
    bpy.context = types.SimpleNamespace(
        preferences=types.SimpleNamespace(addons={"blendeck": types.SimpleNamespace(preferences=prefs)}),
        scene=scene,
        sequencer_scene=None,
        window_manager=types.SimpleNamespace(windows=[]),
    )
    bpy.app = types.SimpleNamespace(version_string="5.2.2")
    bpy.types = types.SimpleNamespace(AddonPreferences=object, Panel=object)
    bpy.props = types.SimpleNamespace(BoolProperty=lambda **k: None, StringProperty=lambda **k: None, IntProperty=lambda **k: None)
    sys.modules["bpy"] = bpy
    return prefs, scene


PREFS, SCENE = _install_fake_bpy()
import blendeck as addon  # noqa: E402


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class Bridge:
    """A stand-in for the bridge: accepts connections and reads lines."""

    def __init__(self):
        self.srv = socket.socket()
        self.srv.bind(("127.0.0.1", 0))
        self.srv.listen(4)
        self.srv.setblocking(False)
        self.port = self.srv.getsockname()[1]

    def accept(self, ticks=200):
        for _ in range(ticks):
            addon.tick()
            try:
                conn, _ = self.srv.accept()
                conn.settimeout(0.3)
                return conn
            except BlockingIOError:
                time.sleep(0.005)
        raise AssertionError("add-on never connected")

    def close(self):
        self.srv.close()


def tick_until(cond, ticks=200):
    for _ in range(ticks):
        addon.tick()
        if cond():
            return True
        time.sleep(0.005)
    return False


def read_lines(conn):
    data = b""
    try:
        while True:
            d = conn.recv(65536)
            if not d:
                break
            data += d
    except socket.timeout:
        pass
    return [json.loads(line) for line in data.decode().splitlines() if line.strip()]


class AddonTest(unittest.TestCase):
    def setUp(self):
        addon._close("reset")
        PREFS.enabled = True
        PREFS.host = "127.0.0.1"
        SCENE.frame_current = 10

    def tearDown(self):
        addon._close("reset")

    def test_closed_port_never_blocks(self):
        PREFS.port = free_port()
        worst = 0.0
        deadline = time.monotonic() + 1.5
        while time.monotonic() < deadline:
            t = time.perf_counter()
            addon.tick()
            worst = max(worst, time.perf_counter() - t)
            time.sleep(0.005)
        self.assertLess(worst, 0.02, "a tick blocked for %.1f ms" % (worst * 1000))
        self.assertIsNone(addon.link.sock)

    def test_hello_state_and_scrub(self):
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        for _ in range(3):
            addon.tick()
        lines = read_lines(conn)
        self.assertEqual(lines[0]["type"], "hello")
        self.assertEqual(lines[1], {"type": "state", "scene": "Edit", "frame": 10, "start": 1, "end": 100, "fps": 24.0})

        conn.sendall(b'{"type":"scrub","frames":10}\n{"type":"scrub","frames":-3}\n')
        self.assertTrue(tick_until(lambda: SCENE.frame_current == 17))
        self.assertEqual(read_lines(conn)[-1]["frame"], 17)
        conn.close()
        bridge.close()

    def test_reconnect_sends_state_again(self):
        bridge = Bridge()
        PREFS.port = bridge.port
        first = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)
        addon.tick()
        first.close()  # bridge restarted, or dropped an idle add-on
        tick_until(lambda: addon.link.sock is None)
        second = bridge.accept(ticks=400)
        tick_until(lambda: addon.link.sock is not None)
        for _ in range(3):
            addon.tick()
        types_seen = [m["type"] for m in read_lines(second)]
        self.assertEqual(types_seen[:2], ["hello", "state"])
        second.close()
        bridge.close()

    def test_turning_off_closes_pending_connection(self):
        PREFS.port = free_port()
        addon.tick()
        PREFS.enabled = False
        addon.tick()
        self.assertIsNone(addon.link.pending)
        self.assertIsNone(addon.link.sock)
        self.assertEqual(addon.link.status, "Turned off")

    def test_hostname_refused_without_lookup(self):
        PREFS.host = "example.com"
        addon.tick()
        self.assertIsNone(addon.link.pending)
        self.assertIn("IP address", addon.link.status)

    def test_tick_never_raises(self):
        orig = addon._tick
        addon._tick = lambda: 1 / 0
        try:
            self.assertEqual(addon.tick(), addon.RETRY)
        finally:
            addon._tick = orig

    def test_unsent_bytes_kept_whole(self):
        bridge = Bridge()
        PREFS.port = bridge.port
        conn = bridge.accept()
        tick_until(lambda: addon.link.sock is not None)

        class Partial:
            """A socket that takes 5 bytes per send, then blocks."""

            def __init__(self, real):
                self.real, self.calls = real, 0

            def send(self, data):
                self.calls += 1
                if self.calls % 2 == 0:
                    raise BlockingIOError
                return self.real.send(data[:5])

            def __getattr__(self, name):
                return getattr(self.real, name)

        addon.link.sock = Partial(addon.link.sock)
        addon._send({"type": "ping"})
        for _ in range(20):
            addon._flush()
        addon.link.sock = addon.link.sock.real
        self.assertIn({"type": "ping"}, read_lines(conn))
        conn.close()
        bridge.close()


if __name__ == "__main__":
    unittest.main()
