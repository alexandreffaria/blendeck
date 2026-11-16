import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "blendeck"))

import protocol  # noqa: E402


class LineBufferTest(unittest.TestCase):
    def test_split_across_reads(self):
        b = protocol.LineBuffer()
        self.assertEqual(b.feed(b'{"type":"scr'), [])
        self.assertEqual(b.feed(b'ub","frames":10}\n{"type":"scrub",'), [{"type": "scrub", "frames": 10}])
        self.assertEqual(b.feed(b'"frames":-1}\n'), [{"type": "scrub", "frames": -1}])

    def test_skips_junk(self):
        b = protocol.LineBuffer()
        self.assertEqual(b.feed(b'nope\n\n[1,2]\n{"type":"x"}\n'), [{"type": "x"}])

    def test_long_line(self):
        b = protocol.LineBuffer()
        with self.assertRaises(ValueError):
            b.feed(b"a" * (protocol.MAX_LINE + 1))


class HintsTest(unittest.TestCase):
    def test_none_without_a_hints_message(self):
        self.assertIsNone(protocol.hints([]))
        self.assertIsNone(protocol.hints([{"type": "scrub", "frames": 1}]))

    def test_lines(self):
        msg = {"type": "hints", "profile": "Video editing", "hints": [
            {"control": "D2", "name": "Scrub timeline", "held": [{"modifier": "Fn", "name": "Scrub timeline (fine)"}]},
            {"control": "C5", "name": "Cut at playhead"},
            {"control": "Exposure", "name": "Trim handle at playhead", "held": [{"modifier": "Fn", "name": "Trim, shrinking one side"}]},
            {"control": "CC 48", "held": [{"modifier": "Fn", "name": "Jump between all cuts"}]},
        ]}
        self.assertEqual(protocol.hints([{"type": "cut"}, msg]), ("Video editing", [
            ("D2", "Scrub timeline · Fn: fine"),
            ("C5", "Cut at playhead"),
            ("Exposure", "Trim handle at playhead · Fn: Trim, shrinking one side"),
            ("CC 48", "Fn: Jump between all cuts"),
        ]))

    def test_newest_wins_and_empty_clears(self):
        old = {"type": "hints", "profile": "A", "hints": [{"control": "K", "name": "Cut"}]}
        new = {"type": "hints", "profile": "B", "hints": []}
        self.assertEqual(protocol.hints([old, new]), ("B", []))

    def test_same_name_held_is_kept(self):
        msg = {"type": "hints", "hints": [{"control": "K", "name": "Zoom", "held": [{"modifier": "Fn", "name": "Zoom"}]}]}
        self.assertEqual(protocol.hints([msg]), ("", [("K", "Zoom · Fn: Zoom")]))

    def test_malformed_is_left_out(self):
        for bad in (None, 3, "x", {"a": 1}, [None, 3, "x", [], {}, {"control": 5, "name": "x"}, {"control": "K"},
                                             {"control": "K", "name": 7, "held": "no"},
                                             {"control": "K", "held": [None, {"modifier": "Fn"}, {"name": "x"}, {"modifier": 1, "name": 2}]}]):
            self.assertEqual(protocol.hints([{"type": "hints", "profile": ["x"], "hints": bad}]), ("", []), bad)

    def test_caps(self):
        long = "x" * 500
        many = [{"control": long, "name": "a\nb\t c", "held": [{"modifier": "M%d" % i, "name": long} for i in range(9)]} for _ in range(500)]
        profile, lines = protocol.hints([{"type": "hints", "profile": long, "hints": many}])
        self.assertEqual(len(profile), protocol.MAX_HINT_LEN)
        self.assertEqual(len(lines), protocol.MAX_HINTS)
        control, text = lines[0]
        self.assertEqual(len(control), protocol.MAX_HINT_LEN)
        self.assertTrue(text.startswith("a b c · M0: "))
        self.assertEqual(text.count("·"), protocol.MAX_HINT_HELD)
        self.assertNotIn("\n", text)


class ScrubTest(unittest.TestCase):
    def test_total(self):
        msgs = [
            {"type": "scrub", "frames": 10},
            {"type": "scrub", "frames": -3},
            {"type": "scrub", "frames": "9"},
            {"type": "scrub", "frames": True},
            {"type": "other", "frames": 100},
            {"type": "scrub"},
        ]
        self.assertEqual(protocol.total_scrub(msgs), 7)

    def test_target_unclamped(self):
        self.assertEqual(protocol.scrub_target(5, -10, 1, 250, False), -5)

    def test_target_clamped(self):
        self.assertEqual(protocol.scrub_target(5, -10, 1, 250, True), 1)
        self.assertEqual(protocol.scrub_target(245, 10, 1, 250, True), 250)
        self.assertEqual(protocol.scrub_target(100, 10, 1, 250, True), 110)

    def test_outside_range_is_not_pulled_back(self):
        # Playhead at 300 with the scene ending at 250: moving left works,
        # moving right stays put instead of jumping to 250.
        self.assertEqual(protocol.scrub_target(300, 10, 1, 250, True), 300)
        self.assertEqual(protocol.scrub_target(300, -10, 1, 250, True), 290)
        self.assertEqual(protocol.scrub_target(-5, -1, 1, 250, True), -5)

    def test_bridge_host(self):
        self.assertEqual(protocol.bridge_host(" 127.0.0.1 "), "127.0.0.1")
        self.assertEqual(protocol.bridge_host("LOCALHOST"), "127.0.0.1")
        self.assertEqual(protocol.bridge_host("::1"), "::1")
        self.assertIsNone(protocol.bridge_host("example.com"))
        self.assertIsNone(protocol.bridge_host(""))

    def test_encode(self):
        self.assertEqual(protocol.encode({"type": "hello"}), b'{"type":"hello"}\n')


class PanTest(unittest.TestCase):
    def test_total(self):
        msgs = [
            {"type": "pan", "clicks": 5},
            {"type": "pan", "clicks": -15},
            {"type": "pan", "clicks": "9"},
            {"type": "pan", "clicks": True},
            {"type": "pan", "clicks": 1.5},
            {"type": "pan"},
            {"type": "zoom", "clicks": 100},
        ]
        self.assertEqual(protocol.total_pan(msgs), -10)

    def test_capped(self):
        self.assertEqual(protocol.total_pan([{"type": "pan", "clicks": 10**6}]), protocol.MAX_PAN_PER_TICK)
        self.assertEqual(protocol.total_pan([{"type": "pan", "clicks": -10**6}]), -protocol.MAX_PAN_PER_TICK)

    def test_others_ignore_pan(self):
        msgs = [{"type": "pan", "clicks": 5, "frames": 5}]
        self.assertEqual(protocol.total_zoom(msgs), 0)
        self.assertEqual(protocol.total_scrub(msgs), 0)
        self.assertEqual(protocol.edit_steps(msgs), [])

    def test_pixels(self):
        self.assertEqual(protocol.pan_pixels(1680, 5), 84)
        self.assertEqual(protocol.pan_pixels(1680, -5), -84)
        self.assertEqual(protocol.pan_pixels(800, 100), 800)
        self.assertEqual(protocol.pan_pixels(10, 1), 0)


class ViewAllTest(unittest.TestCase):
    def test_wants(self):
        self.assertTrue(protocol.wants_view_all([{"type": "scrub", "frames": 1}, {"type": "view_all"}]))
        self.assertTrue(protocol.wants_view_all([{"type": "view_all"}] * 3))
        self.assertFalse(protocol.wants_view_all([{"type": "zoom", "clicks": 1}, {"type": "pan", "clicks": 5}]))
        self.assertFalse(protocol.wants_view_all([]))

    def test_others_ignore_it(self):
        msgs = [{"type": "view_all", "clicks": 5, "frames": 5}]
        self.assertEqual(protocol.total_zoom(msgs), 0)
        self.assertEqual(protocol.total_pan(msgs), 0)
        self.assertEqual(protocol.total_scrub(msgs), 0)
        self.assertEqual(protocol.edit_steps(msgs), [])


class SaveTest(unittest.TestCase):
    def test_wants(self):
        self.assertTrue(protocol.wants_save([{"type": "scrub", "frames": 1}, {"type": "save"}]))
        self.assertTrue(protocol.wants_save([{"type": "save"}] * 3))
        self.assertFalse(protocol.wants_save([{"type": "view_all"}, {"type": "cut"}]))
        self.assertFalse(protocol.wants_save([]))

    def test_others_ignore_it(self):
        msgs = [{"type": "save", "clicks": 5, "frames": 5}]
        self.assertEqual(protocol.total_zoom(msgs), 0)
        self.assertEqual(protocol.total_pan(msgs), 0)
        self.assertEqual(protocol.total_scrub(msgs), 0)
        self.assertEqual(protocol.edit_steps(msgs), [])
        self.assertFalse(protocol.wants_view_all(msgs))


class ZoomTest(unittest.TestCase):
    def test_total(self):
        msgs = [
            {"type": "zoom", "clicks": 3},
            {"type": "zoom", "clicks": -1},
            {"type": "zoom", "clicks": "9"},
            {"type": "zoom", "clicks": True},
            {"type": "zoom", "clicks": 1.5},
            {"type": "zoom"},
            {"type": "scrub", "frames": 10, "clicks": 100},
        ]
        self.assertEqual(protocol.total_zoom(msgs), 2)

    def test_scrub_ignores_zoom(self):
        self.assertEqual(protocol.total_scrub([{"type": "zoom", "clicks": 5, "frames": 5}]), 0)

    def test_total_is_capped(self):
        cap = protocol.MAX_ZOOM_PER_TICK
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": 10**9}]), cap)
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": -(10**9)}]), -cap)
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": 20}] * 5), cap)

    def test_in_and_out_cancel(self):
        self.assertEqual(protocol.total_zoom([{"type": "zoom", "clicks": 4}, {"type": "zoom", "clicks": -4}]), 0)
        self.assertEqual(protocol.total_zoom([]), 0)

    def test_plan(self):
        self.assertEqual(protocol.zoom_plan(3), ("in", 3))
        self.assertEqual(protocol.zoom_plan(-2), ("out", 2))
        self.assertIsNone(protocol.zoom_plan(0))


class CenterPanTest(unittest.TestCase):
    def test_centers(self):
        self.assertEqual(protocol.center_pan(400, 800), 0)
        self.assertEqual(protocol.center_pan(600, 800), 200)
        self.assertEqual(protocol.center_pan(-100, 800), -500)
        self.assertEqual(protocol.center_pan(1300, 801), 900)


class ZoomAnchorTest(unittest.TestCase):
    def test_on_screen_stays_put(self):
        self.assertEqual(protocol.zoom_anchor(0, 800), (0, 0))
        self.assertEqual(protocol.zoom_anchor(150, 800), (0, 150))
        self.assertEqual(protocol.zoom_anchor(800, 800), (0, 800))

    def test_off_screen_centers_first(self):
        # Right of the view: pan right by the distance past the middle
        self.assertEqual(protocol.zoom_anchor(1300, 800), (900, 400))
        # Left of the view: pan left
        self.assertEqual(protocol.zoom_anchor(-50, 800), (-450, 400))
        self.assertEqual(protocol.zoom_anchor(-1, 801), (-401, 400))

    def test_correction(self):
        self.assertEqual(protocol.zoom_correction(170, 150), 20)
        self.assertEqual(protocol.zoom_correction(130, 150), -20)
        self.assertEqual(protocol.zoom_correction(150, 150), 0)
        self.assertEqual(protocol.zoom_correction(150.6, 150), 1)


class CutTest(unittest.TestCase):
    def test_steps_keep_order(self):
        msgs = [
            {"type": "scrub", "frames": 10},
            {"type": "scrub", "frames": 5},
            {"type": "cut"},
            {"type": "zoom", "clicks": 3},
            {"type": "scrub", "frames": -2},
            {"type": "cut"},
        ]
        self.assertEqual(protocol.edit_steps(msgs), [("scrub", 15), ("cut", None), ("scrub", -2), ("cut", None)])

    def test_cuts_in_a_row_are_one(self):
        self.assertEqual(protocol.edit_steps([{"type": "cut"}] * 3), [("cut", None)])

    def test_cuts_capped_scrubs_kept(self):
        msgs = []
        for _ in range(protocol.MAX_PRESSES_PER_TICK + 5):
            msgs += [{"type": "cut"}, {"type": "scrub", "frames": 1}]
        steps = protocol.edit_steps(msgs)
        self.assertEqual(steps.count(("cut", None)), protocol.MAX_PRESSES_PER_TICK)
        self.assertEqual(sum(n for k, n in steps if k == "scrub"), protocol.MAX_PRESSES_PER_TICK + 5)
        self.assertEqual(protocol.presses_dropped(msgs), 5)

    def test_every_press_of_a_freeze_counts_in_order(self):
        # Decision 202611163di7: what piled up while Blender was busy is
        # applied press by press, far past the old 4 undos and 8 cuts
        msgs = []
        for _ in range(10):
            msgs += [{"type": "scrub", "frames": 2}, {"type": "cut"}, {"type": "ripple_delete"}, {"type": "undo"}, {"type": "undo"}]
        self.assertEqual(
            protocol.edit_steps(msgs),
            [("scrub", 2), ("cut", None), ("ripple_delete", None), ("undo", None), ("undo", None)] * 10,
        )
        self.assertEqual(protocol.presses_dropped(msgs), 0)

    def test_press_cap_is_shared_and_keeps_the_first(self):
        cap = protocol.MAX_PRESSES_PER_TICK
        msgs = [{"type": "undo"}] * (cap - 1) + [{"type": "ripple_delete"}, {"type": "cut"}, {"type": "undo"}, {"type": "scrub", "frames": 3}]
        steps = protocol.edit_steps(msgs)
        self.assertEqual(steps, [("undo", None)] * (cap - 1) + [("ripple_delete", None), ("scrub", 3)])
        self.assertEqual(protocol.presses_dropped(msgs), 2)

    def test_merged_cut_is_not_a_dropped_press(self):
        msgs = [{"type": "cut"}] * (protocol.MAX_PRESSES_PER_TICK * 2)
        self.assertEqual(protocol.edit_steps(msgs), [("cut", None)])
        self.assertEqual(protocol.presses_dropped(msgs), 0)

    def test_malformed_ignored(self):
        msgs = [
            {"type": "scrub", "frames": "3"},
            {"type": "scrub", "frames": True},
            {"type": "scrub"},
            {"type": "scrub", "frames": 0},
            {"type": "cutt"},
        ]
        self.assertEqual(protocol.edit_steps(msgs), [])

    def test_steps_match_total_scrub(self):
        msgs = [{"type": "scrub", "frames": 4}, {"type": "cut"}, {"type": "scrub", "frames": 6}]
        self.assertEqual(sum(n for k, n in protocol.edit_steps(msgs) if k == "scrub"), protocol.total_scrub(msgs))

    def test_cuttable(self):
        self.assertTrue(protocol.cuttable(1, 101, 50, False))
        self.assertFalse(protocol.cuttable(1, 101, 50, True), "locked")
        self.assertFalse(protocol.cuttable(50, 101, 50, False), "starts at the playhead")
        self.assertFalse(protocol.cuttable(1, 50, 50, False), "ends at the playhead")
        self.assertFalse(protocol.cuttable(60, 160, 50, False), "after the playhead")
        self.assertTrue(protocol.cuttable(49, 51, 50, False))


class UndoTest(unittest.TestCase):
    def test_every_undo_kept_in_order(self):
        msgs = [
            {"type": "cut"},
            {"type": "undo"},
            {"type": "undo"},
            {"type": "scrub", "frames": 3},
            {"type": "undo"},
            {"type": "cut"},
        ]
        self.assertEqual(
            protocol.edit_steps(msgs),
            [("cut", None), ("undo", None), ("undo", None), ("scrub", 3), ("undo", None), ("cut", None)],
        )

    def test_cut_after_undo_not_merged(self):
        # cut, undo, cut: the second cut does the work again, it isn't a repeat
        msgs = [{"type": "cut"}, {"type": "undo"}, {"type": "cut"}]
        self.assertEqual(protocol.edit_steps(msgs), [("cut", None), ("undo", None), ("cut", None)])

    def test_undos_capped(self):
        n = protocol.MAX_PRESSES_PER_TICK + 20
        steps = protocol.edit_steps([{"type": "undo"}, {"type": "scrub", "frames": 1}] * n)
        self.assertEqual(steps.count(("undo", None)), protocol.MAX_PRESSES_PER_TICK)
        self.assertEqual(sum(n for k, n in steps if k == "scrub"), n)

    def test_malformed_ignored(self):
        self.assertEqual(protocol.edit_steps([{"type": "Undo"}, {"type": "undo "}, {"undo": True}]), [])

    def test_blocked(self):
        self.assertIsNone(protocol.undo_blocked([], False))
        why = protocol.undo_blocked(["TRANSFORM_OT_seq_slide", "TRANSFORM_OT_seq_slide"], False)
        self.assertEqual(why, "an operator is running (TRANSFORM_OT_seq_slide)")
        self.assertEqual(protocol.undo_blocked([], True), "the interface is locked")


S = protocol.Span


class RippleDeleteTest(unittest.TestCase):
    def plan(self, spans, frame):
        return protocol.ripple_plan(spans, frame)

    def test_steps_keep_order_and_never_merge(self):
        msgs = [{"type": "scrub", "frames": 5}, {"type": "ripple_delete"}, {"type": "ripple_delete"}, {"type": "cut"}, {"type": "ripple_delete"}]
        self.assertEqual(
            protocol.edit_steps(msgs),
            [("scrub", 5), ("ripple_delete", None), ("ripple_delete", None), ("cut", None), ("ripple_delete", None)],
        )

    def test_steps_capped_scrubs_kept(self):
        msgs = [{"type": "ripple_delete"}, {"type": "scrub", "frames": 1}] * (protocol.MAX_PRESSES_PER_TICK + 5)
        steps = protocol.edit_steps(msgs)
        self.assertEqual(steps.count(("ripple_delete", None)), protocol.MAX_PRESSES_PER_TICK)
        self.assertEqual(sum(n for k, n in steps if k == "scrub"), protocol.MAX_PRESSES_PER_TICK + 5)
        self.assertEqual(protocol.edit_steps([{"type": "ripple_delete", "x": 1}, {"type": "ripple"}]), [("ripple_delete", None)])

    def test_cut_twice_then_delete_the_middle(self):
        # The owner's workflow: video and audio cut at 50 and 100, playhead
        # on the middle clip; a title later on
        spans = [S(1, 50), S(50, 100), S(100, 200), S(1, 50), S(50, 100), S(100, 200), S(150, 180)]
        p = self.plan(spans, 70)
        self.assertEqual(p.delete, [1, 4])
        self.assertEqual((p.shift, p.playhead), (50, 50))
        self.assertEqual(p.move, [2, 5, 6])

    def test_nothing_under_playhead(self):
        self.assertIsNone(self.plan([S(1, 50), S(60, 100)], 55))
        self.assertIsNone(self.plan([], 10))

    def test_under_means_start_inclusive_end_exclusive(self):
        spans = [S(1, 50), S(50, 100)]
        self.assertEqual(self.plan(spans, 50).delete, [1])
        self.assertEqual(self.plan(spans, 49).delete, [0])

    def test_selected_under_playhead_win(self):
        spans = [S(1, 100, selected=True), S(1, 100), S(200, 300, selected=True)]
        p = self.plan(spans, 50)
        self.assertEqual(p.delete, [0])
        # The unselected strip is kept under the playhead: the gap stays
        self.assertEqual((p.shift, p.move, p.playhead), (0, [], None))
        # A selection elsewhere doesn't count: all under the playhead go
        p = self.plan([S(1, 100), S(1, 100), S(200, 300, selected=True)], 50)
        self.assertEqual((p.delete, p.shift, p.move, p.playhead), ([0, 1], 99, [2], 1))

    def test_locked_never_deleted(self):
        p = self.plan([S(1, 100, locked=True), S(1, 100)], 50)
        self.assertEqual((p.delete, p.shift), ([1], 0))
        self.assertIsNone(self.plan([S(1, 100, locked=True)], 50))

    def test_locked_strip_after_gap_keeps_it_open(self):
        p = self.plan([S(1, 50), S(50, 100), S(120, 150, locked=True), S(100, 140)], 60)
        self.assertEqual((p.delete, p.shift, p.move, p.playhead), ([1], 0, [], None))

    def test_locked_strip_before_gap_is_fine(self):
        p = self.plan([S(1, 50, locked=True), S(50, 100), S(100, 150)], 60)
        self.assertEqual((p.delete, p.shift, p.move, p.playhead), ([1], 50, [2], 50))

    def test_gap_trimmed_to_where_every_channel_is_empty(self):
        # Deleted 0..100; a kept strip ends at 20 and another starts at 70:
        # only 20..70 is empty everywhere, so later strips move by 50 and
        # none lands on a strip that stays
        spans = [S(0, 100), S(0, 20), S(70, 90), S(100, 150)]
        p = self.plan(spans, 40)
        self.assertEqual((p.delete, p.shift, p.playhead), ([0], 50, 20))
        self.assertEqual(p.move, [2, 3])

    def test_moves_never_land_on_a_kept_strip(self):
        # Brute force over small layouts: after the plan, no two strips on
        # one channel overlap
        import itertools
        layouts = list(itertools.product([(0, 10), (10, 20), (20, 30), (5, 25), (0, 30), None], repeat=3))
        for layout in layouts:
            for ch2 in ([(0, 10), (25, 30)], [(15, 30)], [(12, 18)]):
                strips = [(1, a, b) for a, b in filter(None, layout[:1])] + [(1, 30, 40)] + [(2, a, b) for a, b in ch2] + [(3, a, b) for a, b in filter(None, layout[1:])]
                # one channel can't hold overlapping strips to begin with
                if any(c1 == c2 and a1 < b2 and a2 < b1 for i, (c1, a1, b1) in enumerate(strips) for (c2, a2, b2) in strips[i + 1:]):
                    continue
                for frame in range(0, 40, 3):
                    p = self.plan([S(a, b) for _, a, b in strips], frame)
                    if p is None:
                        continue
                    after = []
                    for i, (c, a, b) in enumerate(strips):
                        if i in p.delete:
                            continue
                        d = p.shift if i in p.move else 0
                        after.append((c, a - d, b - d))
                    for i, (c1, a1, b1) in enumerate(after):
                        for c2, a2, b2 in after[i + 1:]:
                            self.assertFalse(c1 == c2 and a1 < b2 and a2 < b1, (strips, frame, p))

    def test_effects_go_with_their_input(self):
        # V under the playhead; X is a crossfade from V into W, past it
        spans = [S(1, 100, selected=True), S(60, 160), S(60, 100, inputs=(0, 1))]
        p = self.plan(spans, 30)
        self.assertEqual(p.delete, [2, 0], "the effect is removed before its input")
        self.assertEqual((p.shift, p.move, p.playhead), (59, [1], 1))

    def test_locked_effect_keeps_its_input(self):
        spans = [S(1, 100), S(1, 100, locked=True, inputs=(0,)), S(1, 100)]
        p = self.plan(spans, 30)
        self.assertEqual((p.delete, p.shift), ([2], 0))

    def test_effect_chain_order(self):
        # 2 is an effect of 1, which is an effect of 0
        spans = [S(1, 100), S(1, 100, inputs=(0,)), S(1, 100, inputs=(1,))]
        self.assertEqual(self.plan(spans, 10).delete, [2, 1, 0])
        # Bad input indexes are ignored
        self.assertEqual(self.plan([S(1, 100, inputs=(5, 0, -1))], 10).delete, [0])


class MoveTest(unittest.TestCase):
    S = protocol.Span

    def plan(self, spans, frames, before=False):
        return protocol.move_plan(spans, frames, before)

    def track(self):
        """Video (1) and audio (2) cut at 50 and 100 with a 20-frame gap
        before the last clips, a title (3) at 150; V2 and A2 selected."""
        S = self.S
        return [
            S(1, 50, channel=1), S(50, 100, selected=True, channel=1), S(120, 200, channel=1),
            S(1, 50, channel=2), S(50, 100, selected=True, channel=2), S(120, 200, channel=2),
            S(150, 180, channel=3),
        ]

    def test_after_moves_selected_and_later_right_freely(self):
        p = self.plan(self.track(), 30)
        self.assertEqual(sorted(p.move), [1, 2, 4, 5, 6])
        self.assertEqual(p.shift, 30)
        # Rightmost first, so none lands on one that hasn't moved
        starts = [self.track()[i].start for i in p.move]
        self.assertEqual(starts, sorted(starts, reverse=True))

    def test_after_stops_against_the_strip_before(self):
        # V1 and A1 end where V2 and A2 start: no gap, nothing moves
        self.assertIsNone(self.plan(self.track(), -1))

    def test_after_closes_a_gap_then_stops(self):
        spans = self.track()
        spans[1] = spans[1]._replace(selected=False)
        spans[4] = spans[4]._replace(selected=False)
        spans[2] = spans[2]._replace(selected=True)  # V3, 20 frames after V2
        p = self.plan(spans, -50)
        self.assertEqual(sorted(p.move), [2, 5, 6])
        self.assertEqual(p.shift, -20, "stops against V2 and A2")
        starts = [spans[i].start for i in p.move]
        self.assertEqual(starts, sorted(starts), "leftmost first going left")

    def test_before_moves_selected_and_earlier(self):
        spans = self.track()
        p = self.plan(spans, 50, before=True)
        self.assertEqual(sorted(p.move), [0, 1, 3, 4])
        self.assertEqual(p.shift, 20, "stops against V3 and A3")
        self.assertEqual(self.plan(spans, -40, before=True).shift, -40, "nothing before them on the left")

    def test_other_channels_dont_block(self):
        S = self.S
        spans = [S(10, 20, selected=True, channel=1), S(25, 30, channel=2), S(5, 40, channel=3)]
        # The title on 3 starts before the selection: it stays, on its own channel
        p = self.plan(spans, 100)
        self.assertEqual(sorted(p.move), [0, 1])
        self.assertEqual(p.shift, 100)

    def test_nothing_selected_or_no_frames(self):
        spans = [s._replace(selected=False) for s in self.track()]
        self.assertIsNone(self.plan(spans, 5))
        self.assertIsNone(self.plan(self.track(), 0))
        self.assertIsNone(self.plan([], 5))

    def test_locked_strips_stay_and_block(self):
        spans = self.track()
        spans[6] = spans[6]._replace(locked=True)  # the title stays
        p = self.plan(spans, 30)
        self.assertEqual(sorted(p.move), [1, 2, 4, 5])
        spans = self.track()
        spans[2] = spans[2]._replace(locked=True)  # V3 stays, 20 frames after V2
        self.assertEqual(self.plan(spans, 30).shift, 20)
        spans = [s._replace(locked=s.selected) for s in self.track()]
        self.assertIsNone(self.plan(spans, 5), "a locked selected strip doesn't count as selected")

    def test_effects_move_with_their_inputs(self):
        S = self.S
        # 0 selected; 2 is an effect of 0 and 1, so 1 (before it) comes along
        spans = [S(50, 100, selected=True, channel=1), S(10, 60, channel=2), S(50, 60, inputs=(0, 1), channel=3), S(0, 5, channel=2)]
        p = self.plan(spans, -100)
        self.assertEqual(sorted(p.move), [0, 1, 2])
        self.assertEqual(p.shift, -5, "1 stops against 3 on channel 2")
        spans[3] = spans[3]._replace(locked=True, start=0, end=5)
        spans[1] = spans[1]._replace(locked=True)
        self.assertIsNone(self.plan(spans, 10), "the group has a locked strip: it stays")

    def test_edit_steps(self):
        steps = protocol.edit_steps([
            {"type": "move", "frames": 1}, {"type": "move", "frames": 2},
            {"type": "move", "frames": -1, "before": True},
            {"type": "scrub", "frames": 3},
            {"type": "move", "frames": 1}, {"type": "move", "frames": -1},
            {"type": "move", "frames": 0}, {"type": "move", "frames": True}, {"type": "move", "frames": 1.5},
            {"type": "move", "frames": 1, "before": 1}, {"type": "move"},
        ])
        self.assertEqual(steps, [("move", 3), ("move_before", -1), ("scrub", 3)])
        flood = [{"type": "move", "frames": 1, "before": i % 2 == 0} for i in range(100)]
        self.assertEqual(len(protocol.edit_steps(flood)), protocol.MAX_MOVES_PER_TICK)


class TrimTest(unittest.TestCase):
    C = protocol.Clip

    def gap(self):
        """The owner's flow: video (1) and its sound (2) with a 20-frame gap
        at 100..120 made by the move knob. Each clip has 30 frames of
        footage hidden on either side."""
        C = self.C
        return [
            C(50, 100, channel=1, lo=20, hi=130), C(120, 200, channel=1, lo=90, hi=230),
            C(50, 100, channel=2, lo=20, hi=130), C(120, 200, channel=2, lo=90, hi=230),
        ]

    def test_start_turned_left_shows_more_of_the_clip(self):
        p = protocol.trim_plan(self.gap(), 120, -5)
        self.assertEqual(p, protocol.TrimPlan([1, 3], [], -5, 115))
        # Into the clip: it gets shorter, down to one frame
        self.assertEqual(protocol.trim_plan(self.gap(), 120, 7), protocol.TrimPlan([1, 3], [], 7, 127))
        self.assertEqual(protocol.trim_plan(self.gap(), 120, 500).shift, 79)

    def test_start_stops_at_the_gap_and_the_footage(self):
        # 20 frames of gap but 30 of footage: the gap wins
        self.assertEqual(protocol.trim_plan(self.gap(), 120, -100).shift, -20)
        clips = self.gap()
        clips[1] = clips[1]._replace(lo=110)  # 10 frames of footage left
        self.assertEqual(protocol.trim_plan(clips, 120, -100).shift, -10)
        clips[1] = clips[1]._replace(lo=120)  # none
        self.assertIsNone(protocol.trim_plan(clips, 120, -1))

    def test_end_mirrors_the_start(self):
        self.assertEqual(protocol.trim_plan(self.gap(), 100, 8), protocol.TrimPlan([], [0, 2], 8, 108))
        self.assertEqual(protocol.trim_plan(self.gap(), 100, 100).shift, 20)
        self.assertEqual(protocol.trim_plan(self.gap(), 100, -100).shift, -49)

    def test_no_handle_at_the_playhead(self):
        self.assertIsNone(protocol.trim_plan(self.gap(), 110, 5))
        self.assertIsNone(protocol.trim_plan(self.gap(), 60, -5))
        self.assertIsNone(protocol.trim_plan(self.gap(), 120, 0))

    def test_where_clips_meet_the_cut_rolls(self):
        C = self.C
        clips = [C(50, 100, channel=1, lo=0, hi=110), C(100, 200, channel=1, lo=95, hi=300)]
        self.assertEqual(protocol.trim_plan(clips, 100, -3), protocol.TrimPlan([1], [0], -3, 97))
        # The incoming clip has 5 frames of footage before, the outgoing 10 after
        self.assertEqual(protocol.trim_plan(clips, 100, -50).shift, -5)
        self.assertEqual(protocol.trim_plan(clips, 100, 50).shift, 10)

    def test_locked_and_effects_stay(self):
        C = self.C
        clips = [C(50, 100, channel=1), C(100, 200, channel=1, locked=True), C(100, 200, channel=3, follows=True)]
        # Only the end of the first moves, and only left: the locked clip is in the way
        self.assertEqual(protocol.trim_plan(clips, 100, -4), protocol.TrimPlan([], [0], -4, 96))
        self.assertIsNone(protocol.trim_plan(clips, 100, 4))
        self.assertIsNone(protocol.trim_plan(clips, 200, 4), "nothing unlocked ends there")

    def test_an_effect_stops_at_its_own_neighbors(self):
        C = self.C
        # A transform on V2 (channel 3, following it) with a title 5
        # frames before it on channel 3
        clips = self.gap() + [C(120, 200, channel=3, follows=True), C(100, 115, channel=3)]
        self.assertEqual(protocol.trim_plan(clips, 120, -100).shift, -5)
        # A neighbor touching the effect rolls with it instead: it ends at
        # the playhead too, and stops one frame from its own start
        clips[5] = C(100, 120, channel=3)
        self.assertEqual(protocol.trim_plan(clips, 120, -100), protocol.TrimPlan([1, 3], [5], -19, 101))

    def test_no_limit_without_footage(self):
        C = self.C
        clips = [C(10, 20, channel=4)]  # a color strip
        self.assertEqual(protocol.trim_plan(clips, 10, -500), protocol.TrimPlan([0], [], -500, -490))
        self.assertEqual(protocol.trim_plan(clips, 20, 500).shift, 500)

    def test_steps_add_up_and_cap(self):
        steps = protocol.edit_steps([{"type": "trim", "frames": -1}, {"type": "trim", "frames": -2}, {"type": "scrub", "frames": 1}, {"type": "trim", "frames": 1}])
        self.assertEqual(steps, [("trim", -3), ("scrub", 1), ("trim", 1)])
        self.assertEqual(protocol.edit_steps([{"type": "trim", "frames": 1}, {"type": "trim", "frames": -1}]), [])
        bad = [{"type": "trim"}, {"type": "trim", "frames": True}, {"type": "trim", "frames": 1.5}, {"type": "trim", "frames": 0}]
        self.assertEqual(protocol.edit_steps(bad), [])
        flood = [{"type": "trim", "frames": 1}] * (protocol.MAX_TRIMS_PER_TICK + 10)
        self.assertEqual(protocol.edit_steps(flood), [("trim", protocol.MAX_TRIMS_PER_TICK)])

    def test_shrink_steps_are_their_own_kind(self):
        S = {"type": "trim", "frames": 1, "shrink": True}
        steps = protocol.edit_steps([S, S, {"type": "trim", "frames": 1}, dict(S, shrink=1), S])
        # Only shrink: true counts as shrinking; a truthy non-bool is a plain trim
        self.assertEqual(steps, [("trim_shrink", 2), ("trim", 2), ("trim_shrink", 1)])
        # A shrink the other way is not taken off the one before it
        back = dict(S, frames=-1)
        self.assertEqual(protocol.edit_steps([dict(S, frames=3), back]), [("trim_shrink", 3), ("trim_shrink", -1)])
        self.assertEqual(protocol.edit_steps([S, S, back, back]), [("trim_shrink", 2), ("trim_shrink", -2)])
        flood = [S, {"type": "trim", "frames": 1}] * protocol.MAX_TRIMS_PER_TICK
        self.assertEqual(len(protocol.edit_steps(flood)), protocol.MAX_TRIMS_PER_TICK, "both kinds share the cap")

    # Decision 202611150cft: with Fn, only the clip turned into gets shorter.
    def joined(self):
        C = self.C
        return [C(50, 100, channel=1, lo=0, hi=110), C(100, 200, channel=1, lo=95, hi=300),
                C(50, 100, channel=2, lo=0, hi=110), C(100, 200, channel=2, lo=95, hi=300)]

    def test_shrink_at_a_join_opens_a_gap(self):
        # Right shortens the next clips' starts only; the ends stay
        self.assertEqual(protocol.trim_plan(self.joined(), 100, 4, shrink=True), protocol.TrimPlan([1, 3], [], 4, 104))
        # Left shortens the first clips' ends only
        self.assertEqual(protocol.trim_plan(self.joined(), 100, -4, shrink=True), protocol.TrimPlan([], [0, 2], -4, 96))
        # No footage limit applies to a handle going inwards: down to one frame
        self.assertEqual(protocol.trim_plan(self.joined(), 100, 500, shrink=True).shift, 99)
        self.assertEqual(protocol.trim_plan(self.joined(), 100, -500, shrink=True).shift, -49)
        # Without shrink it still rolls
        self.assertEqual(protocol.trim_plan(self.joined(), 100, 4), protocol.TrimPlan([1, 3], [0, 2], 4, 104))

    def test_shrink_never_grows_a_lone_handle(self):
        # A start after the gap: right shrinks it, left (growing) does nothing
        self.assertEqual(protocol.trim_plan(self.gap(), 120, 3, shrink=True), protocol.TrimPlan([1, 3], [], 3, 123))
        self.assertIsNone(protocol.trim_plan(self.gap(), 120, -3, shrink=True))
        self.assertEqual(protocol.trim_plan(self.gap(), 100, -3, shrink=True), protocol.TrimPlan([], [0, 2], -3, 97))
        self.assertIsNone(protocol.trim_plan(self.gap(), 100, 3, shrink=True))

    def test_shrink_ignores_effects_on_the_side_that_stays(self):
        C = self.C
        # A transform following the first clip ends at the join, with a
        # locked title starting there on its channel: shrinking the next clip's
        # start doesn't move the transform, so the title is no limit
        clips = self.joined() + [C(50, 100, channel=3, follows=True), C(100, 130, channel=3, locked=True)]
        self.assertEqual(protocol.trim_plan(clips, 100, 4, shrink=True).shift, 4)
        # Rolling moves the transform's end too, into the title: blocked
        self.assertIsNone(protocol.trim_plan(clips, 100, 4))


class SelectionTest(unittest.TestCase):
    def spans(self):
        S = protocol.Span
        return [
            S(1, 101),  # 0: under the playhead
            S(1, 101, locked=True),  # 1: under, locked
            S(1, 101, selected=True),  # 2: under, selected
            S(60, 160),  # 3: after
            S(1, 50),  # 4: ends at the playhead
            S(50, 90),  # 5: starts at the playhead
        ]

    def test_cut_targets_selected_only(self):
        self.assertEqual(protocol.cut_targets(self.spans(), 50), [2])

    def test_cut_targets_all_when_none_selected(self):
        spans = [s._replace(selected=False) for s in self.spans()]
        # Strictly inside: 5 starts at 50 and isn't cut
        self.assertEqual(protocol.cut_targets(spans, 50), [0, 2])

    def test_cut_targets_selection_outside_playhead_ignored(self):
        spans = [s._replace(selected=False) for s in self.spans()]
        spans[3] = spans[3]._replace(selected=True)
        self.assertEqual(protocol.cut_targets(spans, 50), [0, 2])

    def test_cut_targets_nothing(self):
        self.assertEqual(protocol.cut_targets([], 50), [])
        self.assertEqual(protocol.cut_targets([protocol.Span(1, 101, locked=True)], 50), [])

    def test_follow_selection(self):
        self.assertEqual(protocol.follow_selection(self.spans(), 50), [True, False, True, False, False, True])
        self.assertEqual(protocol.follow_selection(self.spans(), 200), [False] * 6)
        self.assertEqual(protocol.follow_selection([], 50), [])


class PlayPauseTest(unittest.TestCase):
    P = {"type": "play_pause"}

    def test_steps_keep_order(self):
        msgs = [{"type": "scrub", "frames": 10}, self.P, {"type": "scrub", "frames": 5}, {"type": "cut"}, self.P]
        self.assertEqual(protocol.edit_steps(msgs), [("scrub", 10), ("play_pause", None), ("scrub", 5), ("cut", None), ("play_pause", None)])

    def test_presses_in_a_row_cancel_in_pairs(self):
        self.assertEqual(protocol.edit_steps([self.P]), [("play_pause", None)])
        self.assertEqual(protocol.edit_steps([self.P, self.P]), [])
        self.assertEqual(protocol.edit_steps([self.P] * 3), [("play_pause", None)])
        # Anything between keeps them apart: play, scrub, pause
        self.assertEqual(protocol.edit_steps([self.P, {"type": "scrub", "frames": 1}, self.P]), [("play_pause", None), ("scrub", 1), ("play_pause", None)])

    def test_presses_capped(self):
        msgs = [self.P, {"type": "scrub", "frames": 1}] * 30
        steps = protocol.edit_steps(msgs)
        self.assertEqual(sum(1 for k, _ in steps if k == "play_pause"), protocol.MAX_TOGGLES_PER_TICK)
        self.assertEqual(sum(f for k, f in steps if k == "scrub"), 30, "scrubs past the cap still count")

    def test_malformed_ignored(self):
        self.assertEqual(protocol.edit_steps([{"type": "play"}, {"type": "PLAY_PAUSE"}]), [])

    def test_plan(self):
        # Playing anywhere: pause, wherever it plays
        self.assertEqual(protocol.play_pause_plan(True, False, False), "pause")
        self.assertEqual(protocol.play_pause_plan(True, True, True), "pause")
        # Not playing: from a video editor, else the window, else nothing
        self.assertEqual(protocol.play_pause_plan(False, True, True), "play")
        self.assertEqual(protocol.play_pause_plan(False, True, False), "play")
        self.assertEqual(protocol.play_pause_plan(False, False, True), "play_window")
        self.assertIsNone(protocol.play_pause_plan(False, False, False))


class ShuttleTest(unittest.TestCase):
    F = {"type": "shuttle", "clicks": 1}
    B = {"type": "shuttle", "clicks": -1}

    def test_speed_steps(self):
        sp = protocol.shuttle_speed
        # Stopped: normal speed that way
        self.assertEqual(sp(0, 1), 100)
        self.assertEqual(sp(0, -1), -100)
        # Already going that way: 0.25x faster each press
        self.assertEqual(sp(100, 1), 125)
        self.assertEqual(sp(-100, -1), -125)
        self.assertEqual(sp(-150, -1), -175)
        # The other way, faster than 1x: ease off 0.25x a press, down to 1x
        self.assertEqual(sp(250, -1), 225)
        self.assertEqual(sp(-250, 1), -225)
        self.assertEqual(sp(125, -1), 100)
        self.assertEqual(sp(-125, 1), -100)
        # The other way at 1x: normal speed in the new direction
        self.assertEqual(sp(100, -1), -100)
        self.assertEqual(sp(-100, 1), 100)
        # From 1x: 16 presses to 5x, 28 to 8x, the top
        s = 100
        for _ in range(16):
            s = sp(s, 1)
        self.assertEqual(s, 500)
        for _ in range(12):
            s = sp(s, 1)
        self.assertEqual(s, 800)
        self.assertEqual(protocol.MAX_SHUTTLE_SPEED, 800)
        self.assertEqual(sp(s, 1), 800)
        self.assertEqual(sp(-800, -1), -800)
        # From 8x forward: 28 presses down to 1x, the 29th reverses
        for _ in range(28):
            s = sp(s, -1)
        self.assertEqual(s, 100)
        self.assertEqual(sp(s, -1), -100)

    def test_frame_follows_the_clock(self):
        f = protocol.shuttle_frame
        # 24 fps: 1.5x for 2 s is 72 frames, backwards 2x for 1 s is 48 back
        self.assertEqual(f(100, 2.0, 24, 150, 1, 1000), (172, False))
        self.assertEqual(f(100, 1.0, 24, -200, 1, 1000), (52, False))
        self.assertEqual(f(100, 0.0, 24, 110, 1, 1000), (100, False))
        # Fractions round down, both ways
        self.assertEqual(f(100, 0.01, 24, 100, 1, 1000), (100, False))
        self.assertEqual(f(100, 0.01, 24, -100, 1, 1000), (99, False))

    def test_frame_stops_at_the_ends(self):
        f = protocol.shuttle_frame
        self.assertEqual(f(990, 10.0, 24, 300, 1, 1000), (1000, True))
        self.assertEqual(f(10, 10.0, 24, -300, 1, 1000), (1, True))
        # At the start going forward, or at the end going backwards, is fine
        self.assertEqual(f(1, 0.0, 24, 100, 1, 1000), (1, False))
        self.assertEqual(f(1000, 0.0, 24, -100, 1, 1000), (1000, False))
        # Outside the range: runs on to where it started
        self.assertEqual(f(1200, 1.0, 24, 100, 1, 1000), (1200, True))
        self.assertEqual(f(1200, 1.0, 24, -100, 1, 1000), (1176, False))

    def test_steps_keep_order_never_merged(self):
        msgs = [self.F, self.F, {"type": "scrub", "frames": 3}, self.B, {"type": "play_pause"}, self.F]
        self.assertEqual(protocol.edit_steps(msgs), [("shuttle", 1), ("shuttle", 1), ("scrub", 3), ("shuttle", -1), ("play_pause", None), ("shuttle", 1)])

    def test_steps_capped_and_malformed_ignored(self):
        steps = protocol.edit_steps([self.F] * 30)
        self.assertEqual(len(steps), protocol.MAX_SHUTTLES_PER_TICK)
        bad = [{"type": "shuttle"}, {"type": "shuttle", "clicks": 2}, {"type": "shuttle", "clicks": 0}, {"type": "shuttle", "clicks": True}, {"type": "shuttle", "clicks": "1"}, {"type": "shuttle", "clicks": 1.0}]
        self.assertEqual(protocol.edit_steps(bad), [])


class AudioPlanTest(unittest.TestCase):
    # 24 fps; strips are (key, start, end, content_start, sound_offset)
    def test_which_speeds_have_sound(self):
        self.assertTrue(protocol.shuttle_has_audio(100))  # the add-on's own 1x
        self.assertTrue(protocol.shuttle_has_audio(125))
        self.assertTrue(protocol.shuttle_has_audio(500))
        self.assertFalse(protocol.shuttle_has_audio(525))
        self.assertFalse(protocol.shuttle_has_audio(0))
        self.assertFalse(protocol.shuttle_has_audio(-100))

    def test_from_inside_a_strip(self):
        # Strip on frames 100-340, its sound starting at frame 40, 1 s cut
        # off the front of the file: the playhead on 160 is 5 + 1 s in
        strips = [("a", 100, 340, 40, 1.0)]
        plan = protocol.audio_plan(strips, 160, 0.0, 24, 200, set(), 0)
        self.assertEqual(plan, [("a", 6.0, 13.5, 0.0)])

    def test_waits_for_a_strip_ahead(self):
        # At 2x, 48 frames a second: frame 172 is heard in 0.25 s
        strips = [("a", 172, 200, 172, 0.0)]
        self.assertEqual(protocol.audio_plan(strips, 160, 0.0, 24, 200, set(), 0), [("a", 0.0, 28 / 24, 0.25)])

    def test_only_what_is_heard_soon(self):
        # 2 s ahead at 2x is 96 frames: up to 256 from 160
        strips = [("past", 0, 160, 0, 0.0), ("soon", 250, 300, 250, 0.0), ("later", 256, 300, 256, 0.0)]
        plan = protocol.audio_plan(strips, 100, 1.25, 24, 200, set(), 0)  # 60 frames on: 160
        self.assertEqual([p[0] for p in plan], ["soon"])

    def test_skips_started_and_caps(self):
        strips = [(i, 100 + i, 400, 100, 0.0) for i in range(40)]
        plan = protocol.audio_plan(strips, 100, 0.0, 24, 100, {0, 1}, 0)
        self.assertEqual(len(plan), protocol.MAX_AUDIO_SOUNDS)
        self.assertEqual(plan[0][0], 2)
        self.assertEqual(protocol.audio_plan(strips, 100, 0.0, 24, 100, set(), protocol.MAX_AUDIO_SOUNDS), [])

    def test_silent_speeds_plan_nothing(self):
        strips = [("a", 0, 1000, 0, 0.0)]
        self.assertEqual(protocol.audio_plan(strips, 100, 0.0, 24, 525, set(), 0), [])
        self.assertEqual(protocol.audio_plan(strips, 100, 0.0, 24, -200, set(), 0), [])
        self.assertEqual(protocol.audio_plan(strips, 100, 0.0, 0, 200, set(), 0), [])


class ScrubSoundsTest(unittest.TestCase):
    # 24 fps; strips are (key, start, end, content_start, sound_offset)
    def test_frames_moved_over_landing_frame_included(self):
        self.assertEqual(protocol.scrub_frames(100, 110, 24), (101, 111))  # forward: ends on the playhead
        self.assertEqual(protocol.scrub_frames(110, 100, 24), (100, 110))  # backwards: starts there
        self.assertEqual(protocol.scrub_frames(100, 101, 24), (101, 102))  # Fn: just the landing frame
        self.assertEqual(protocol.scrub_frames(101, 100, 24), (100, 101))
        self.assertIsNone(protocol.scrub_frames(100, 100, 24))
        self.assertIsNone(protocol.scrub_frames(100, 110, 0))

    def test_a_long_move_plays_the_part_nearest_the_landing_frame(self):
        # 0.5 s at 24 fps is 12 frames
        self.assertEqual(protocol.scrub_frames(100, 140, 24), (129, 141))
        self.assertEqual(protocol.scrub_frames(140, 100, 24), (100, 112))

    def test_each_strip_in_the_stretch_plays_in_turn(self):
        # A ends on 105, B starts there, C starts after the stretch
        strips = [("b", 105, 300, 105, 0.0), ("a", 50, 105, 20, 1.0), ("c", 111, 300, 111, 0.0)]
        plan = protocol.scrub_sounds(strips, 100, 110, 24)
        self.assertEqual(plan, [("a", 81 / 24 + 1.0, 85 / 24 + 1.0, 0.0), ("b", 0.0, 6 / 24, 4 / 24)])

    def test_caps_and_no_move(self):
        strips = [(i, 0, 400, 0, 0.0) for i in range(40)]
        self.assertEqual(len(protocol.scrub_sounds(strips, 10, 20, 24)), protocol.MAX_AUDIO_SOUNDS)
        self.assertEqual(protocol.scrub_sounds(strips, 10, 10, 24), [])
        self.assertEqual(protocol.scrub_sounds(strips, 10, 20, 0), [])


class ManifestTest(unittest.TestCase):
    def test_manifest_matches_addon(self):
        import re
        import tomllib

        here = os.path.join(os.path.dirname(__file__), "..", "blendeck")
        with open(os.path.join(here, "blender_manifest.toml"), "rb") as f:
            manifest = tomllib.load(f)
        for key in ("schema_version", "id", "version", "name", "tagline", "maintainer", "type", "blender_version_min", "license"):
            self.assertIn(key, manifest)
        with open(os.path.join(here, "__init__.py")) as f:
            m = re.search(r'ADDON_VERSION = "([^"]+)"', f.read())
        self.assertEqual(m.group(1), manifest["version"], "ADDON_VERSION and the manifest disagree")


class JumpTest(unittest.TestCase):
    def test_edit_points(self):
        self.assertEqual(protocol.edit_points([(1, 50), (50, 100), (20, 80), (100, 200)]), [1, 20, 50, 80, 100, 200])
        self.assertEqual(protocol.edit_points([]), [])

    def test_jump_target(self):
        pts = [1, 20, 50, 80, 100, 200]
        self.assertEqual(protocol.jump_target(pts, 30, 1), 50)
        self.assertEqual(protocol.jump_target(pts, 30, -1), 20)
        # On a cut: the next one, not the same one
        self.assertEqual(protocol.jump_target(pts, 50, 1), 80)
        self.assertEqual(protocol.jump_target(pts, 50, -1), 20)
        self.assertEqual(protocol.jump_target(pts, 30, 3), 100)
        self.assertEqual(protocol.jump_target(pts, 30, -2), 1)
        # Past the last cut that way: stops on the last one
        self.assertEqual(protocol.jump_target(pts, 30, 99), 200)
        self.assertEqual(protocol.jump_target(pts, 30, -99), 1)
        # None that way: stays
        self.assertIsNone(protocol.jump_target(pts, 200, 1))
        self.assertIsNone(protocol.jump_target(pts, 250, 1))
        self.assertIsNone(protocol.jump_target(pts, 1, -1))
        self.assertEqual(protocol.jump_target(pts, -10, 1), 1)
        self.assertEqual(protocol.jump_target(pts, 250, -1), 200)
        self.assertIsNone(protocol.jump_target([], 10, 1))
        self.assertIsNone(protocol.jump_target(pts, 30, 0))

    def test_edit_steps_jumps(self):
        J = lambda n: {"type": "jump", "clicks": n}
        self.assertEqual(protocol.edit_steps([J(1), J(1), J(-1)]), [("jump", 1)])
        self.assertEqual(protocol.edit_steps([J(1), J(-1)]), [], "right then left: nothing")
        # A cut between keeps them apart, in order
        self.assertEqual(protocol.edit_steps([J(1), {"type": "cut"}, J(2)]), [("jump", 1), ("cut", None), ("jump", 2)])
        self.assertEqual(protocol.edit_steps([{"type": "scrub", "frames": 3}, J(-1)]), [("scrub", 3), ("jump", -1)])
        S = lambda n: {"type": "jump", "clicks": n, "sound": True}
        self.assertEqual(protocol.edit_steps([J(1), S(1), S(1), J(-1)]), [("jump", 1), ("jump_sound", 2), ("jump", -1)])
        self.assertEqual(protocol.edit_steps([S(1), S(-1)]), [])
        self.assertEqual(protocol.edit_steps([{"type": "jump", "clicks": 1, "sound": False}]), [("jump", 1)])
        for bad in ({"type": "jump", "clicks": 1, "sound": 1}, {"type": "jump", "clicks": 1, "sound": "yes"}, {"type": "jump", "clicks": 1, "sound": None}):
            self.assertEqual(protocol.edit_steps([bad]), [], bad)
        self.assertEqual(protocol.edit_steps([S(1)] * (protocol.MAX_JUMPS_PER_TICK + 5) + [J(1)]), [("jump_sound", protocol.MAX_JUMPS_PER_TICK)])
        for bad in (J(0), J(True), J("1"), J(1.0), J(None), {"type": "jump"}):
            self.assertEqual(protocol.edit_steps([bad]), [], bad)
        # A flood is capped, either way, per read
        cap = protocol.MAX_JUMPS_PER_TICK
        self.assertEqual(protocol.edit_steps([J(1)] * (cap + 50)), [("jump", cap)])
        self.assertEqual(protocol.edit_steps([J(-1000)]), [("jump", -cap)])
        self.assertEqual(protocol.edit_steps([J(cap), {"type": "cut"}, J(1)]), [("jump", cap), ("cut", None)])


class LatencyTest(unittest.TestCase):
    def test_oldest_stamp(self):
        msgs = [{"type": "scrub", "frames": 1, "t": 5000}, {"type": "cut", "t": 4000}, {"type": "undo"}]
        self.assertEqual(protocol.oldest_stamp(msgs), 4000)
        self.assertIsNone(protocol.oldest_stamp([]))
        self.assertIsNone(protocol.oldest_stamp([{"type": "cut"}]))

    def test_oldest_stamp_ignores_nonsense(self):
        bad = [{"t": "soon"}, {"t": True}, {"t": -5}, {"t": 0}, {"t": None}, {"t": [1]}, {"t": float("nan")}]
        self.assertIsNone(protocol.oldest_stamp(bad))
        self.assertEqual(protocol.oldest_stamp(bad + [{"t": 7.5}]), 7.5)

    def test_moves_picture(self):
        for kind in ("cut", "undo", "ripple_delete", "move", "trim"):
            self.assertTrue(protocol.moves_picture([{"type": "zoom", "clicks": 1}, {"type": kind}]), kind)
        self.assertFalse(protocol.moves_picture([{"type": "zoom", "clicks": 1}, {"type": "pan", "clicks": 1}, {}]))

    def test_report(self):
        report = protocol.latency_report([4.04, 9, 2], [11, 170.26], [15, 179], 171.449, 0)
        self.assertEqual(
            report,
            {
                "type": "latency", "samples": 3, "wait_med": 4.0, "wait_max": 9, "picture_med": 170.3,
                "picture_max": 170.3, "total_med": 179, "total_max": 179, "tick_max": 171.4, "dropped": 0,
            },
        )
        protocol.encode(report)  # goes over the wire as it is

    def test_report_without_pictures(self):
        # No preview on screen: the waits are still worth reporting
        report = protocol.latency_report([3], [], [], 12, 0)
        self.assertEqual((report["samples"], report["picture_max"], report["total_max"]), (1, 0.0, 0.0))

    def test_nothing_to_report(self):
        self.assertIsNone(protocol.latency_report([], [], [], 500, 0))
        self.assertEqual(protocol.latency_report([], [], [], 0, 3)["dropped"], 3)


if __name__ == "__main__":
    unittest.main()
