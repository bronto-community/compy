//go:build darwin

package window

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// compy_install_main_menu gives the app a minimal main menu. Without one,
// macOS never routes the standard key equivalents (Cmd+C/V/X/A/Z) to the
// WKWebView — only the right-click menu works. The items target the first
// responder (nil target), which is how the standard Edit menu works in any
// AppKit app; WKWebView implements all six selectors.
static void compy_install_main_menu(void) {
	NSMenu *mainMenu = [[NSMenu alloc] init];

	// App menu: Quit (Cmd+Q).
	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	[mainMenu addItem:appItem];
	NSMenu *appMenu = [[NSMenu alloc] init];
	[appMenu addItemWithTitle:@"Quit compy" action:@selector(terminate:) keyEquivalent:@"q"];
	[appItem setSubmenu:appMenu];

	// Edit menu: the reason this file exists.
	NSMenuItem *editItem = [[NSMenuItem alloc] init];
	[mainMenu addItem:editItem];
	NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
	[editMenu addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
	[editMenu addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"Z"]; // uppercase = Shift+Cmd+Z
	[editMenu addItem:[NSMenuItem separatorItem]];
	[editMenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
	[editMenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
	[editMenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
	[editMenu addItem:[NSMenuItem separatorItem]];
	[editMenu addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
	[editItem setSubmenu:editMenu];

	[NSApp setMainMenu:mainMenu];
}
*/
import "C"

// installMainMenu installs the app/Edit main menu. Call after webview.New
// (which finishes NSApplication launch) and before Run; webview_go never
// sets a main menu of its own, so there is nothing to fight.
func installMainMenu() { C.compy_install_main_menu() }
