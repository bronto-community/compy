//go:build darwin

// In-menu key equivalents. systray v1.12.2 creates every darwin NSMenuItem
// with keyEquivalent:@"" and offers no way to change it, so this file is
// the gap-fill for that one missing library feature — not a new platform
// layer. The seam it uses: systray stamps each NSMenuItem's tag with its
// Go-side menu id (add_or_update_menu_item does setTag:) and, on the
// systray.Run path, installs its SystrayAppDelegate — whose `menu` ivar is
// the status item's NSMenu — as NSApp.delegate. So: read the unexported id
// off the *systray.MenuItem, find the NSMenuItem by tag, set the
// equivalent. systray's item updates (SetTitle/Enable/…) never touch
// keyEquivalent, so an application sticks for the life of the process —
// everything (static items and config-row digits alike) is set once at
// build, no re-apply on sync needed.
package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>

// compy_systray_menu resolves systray's NSMenu, or nil when the delegate
// is not systray's (an external-loop embedding) — then shortcuts silently
// degrade and the menu still works. Main thread only.
static NSMenu *compy_systray_menu(void) {
	id delegate = [NSApp delegate];
	Class c = NSClassFromString(@"SystrayAppDelegate");
	if (c == nil || delegate == nil || ![delegate isKindOfClass:c]) {
		return nil;
	}
	id menu = [delegate valueForKey:@"menu"]; // KVC falls through to the ivar
	if (![menu isKindOfClass:[NSMenu class]]) {
		return nil;
	}
	return (NSMenu *)menu;
}

// compy_set_key_equivalent sets the key equivalent on the top-level menu
// item tagged tag. cmd selects the ⌘ modifier; without it the mask is 0 and
// the bare key renders right-aligned, like Apple's own menus. Hops to the
// main queue (AppKit rule); an unknown tag is a no-op.
static void compy_set_key_equivalent(int tag, const char *ckey, bool cmd) {
	NSString *key = [NSString stringWithUTF8String:ckey];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSMenuItem *item = [compy_systray_menu() itemWithTag:tag];
		if (item == nil) {
			return;
		}
		item.keyEquivalent = key;
		item.keyEquivalentModifierMask = cmd ? NSEventModifierFlagCommand : 0;
	});
}

// compy_key_equivalent_for reads back what AppKit holds for tag as
// "key/mask" ("" when absent); caller frees. Verification only — the
// dispatch_sync also serializes it after any pending sets.
static char *compy_key_equivalent_for(int tag) {
	__block char *out = strdup("");
	dispatch_sync(dispatch_get_main_queue(), ^{
		NSMenuItem *item = [compy_systray_menu() itemWithTag:tag];
		if (item == nil) {
			return;
		}
		free(out);
		out = strdup([[NSString stringWithFormat:@"%@/%lu", item.keyEquivalent,
			(unsigned long)item.keyEquivalentModifierMask] UTF8String]);
	});
	return out;
}
*/
import "C"

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"unsafe"

	"fyne.io/systray"
)

// keyEquiv is one shortcut assignment; cmd marks the ⌘ modifier (plain keys
// carry mask 0).
type keyEquiv struct {
	item *systray.MenuItem
	key  string
	cmd  bool
}

// keyEquivalents is the static half of the shortcut map (no global hotkey —
// Ctrl+F8 is macOS's own keyboard path into menu extras): s on Stop/Start,
// r on Restart (disabled items never fire, so r is inert while stopped),
// o on Open compy, ⌘Q on Quit. Plain-key equivalents override the menu's
// type-to-select — accepted trade. Remove from Menu Bar gets no key on
// purpose: it is the destructive one. The config-row digits are the other
// half — digitEquivalents, equally static.
func keyEquivalents(toggle, restart, open, quit *systray.MenuItem) []keyEquiv {
	return []keyEquiv{
		{toggle, "s", false},
		{restart, "r", false},
		{open, "o", false},
		{quit, "q", true},
	}
}

// digitEquivalents assigns digits 1–9 to the first nine inline config
// slots, submenu rows included: digit d always means "activate the d-th
// row's config with its current preset". On a preset-submenu parent the
// digit fires the parent's action during tracking (measured 2026-08-29 on
// macOS 26.5.2 with a throwaway status-item app) — systray delivers that as
// a slot click, and handleSlotClicks resolves the preset at click time, so
// the digit does on a multi-preset row exactly what a mouse click does on a
// single-preset one. It does NOT open the submenu (AppKit fires the action
// and closes the menu; no public API opens a submenu from a key), and
// AppKit draws no glyph on submenu rows (the chevron takes the slot — no
// overlap, the digit is just invisible there; positional numbering keeps it
// guessable). Presets inside submenus deliberately carry NO digits: a
// closed submenu's equivalents fire from the parent level in depth-first
// menu order — a preset "3" inside row 2 would steal digit 3 from the
// visible row 3 — and once a submenu is open, keyboard navigation swallows
// plain keys entirely, so preset digits would misfire when closed and be
// dead when open (same empirics). Applied once at build: slots sit at
// fixed menu positions and hidden slots keep their NSMenuItem, so digits
// never need re-applying; a resort retargets for free because handlers
// resolve slotNames[i] at click time (TestDigitsRetargetOnResort).
func digitEquivalents(slots []*systray.MenuItem) []keyEquiv {
	var out []keyEquiv
	for i, slot := range slots {
		if i >= 9 {
			break
		}
		out = append(out, keyEquiv{slot, strconv.Itoa(i + 1), false})
	}
	return out
}

// menuID reads a systray.MenuItem's unexported id — the NSMenuItem tag on
// darwin. false when a systray upgrade renamed the field; callers then
// skip, losing shortcuts and nothing else (TestMenuIDField pins the seam).
func menuID(item *systray.MenuItem) (uint32, bool) {
	v := reflect.ValueOf(item).Elem().FieldByName("id")
	if !v.IsValid() || v.Kind() != reflect.Uint32 {
		return 0, false
	}
	return uint32(v.Uint()), true
}

// applyKeyEquivalents pushes the shortcut map into AppKit once the menu is
// built. With COMPY_TRAY_DEBUG_KEYS set it then reads each equivalent back
// off the live NSMenu and prints it to stderr — the closest introspection
// gets to "the open menu really carries them" without an accessibility
// harness.
func applyKeyEquivalents(eqs []keyEquiv) {
	for _, e := range eqs {
		id, ok := menuID(e.item)
		if !ok || id == 0 {
			continue
		}
		ckey := C.CString(e.key)
		C.compy_set_key_equivalent(C.int(id), ckey, C.bool(e.cmd))
		C.free(unsafe.Pointer(ckey))
	}
	if os.Getenv("COMPY_TRAY_DEBUG_KEYS") == "" {
		return
	}
	for _, e := range eqs {
		id, ok := menuID(e.item)
		if !ok {
			continue
		}
		c := C.compy_key_equivalent_for(C.int(id))
		fmt.Fprintf(os.Stderr, "compy tray: key equivalent tag=%d want=%q cmd=%v holds=%q\n", id, e.key, e.cmd, C.GoString(c))
		C.free(unsafe.Pointer(c))
	}
}
