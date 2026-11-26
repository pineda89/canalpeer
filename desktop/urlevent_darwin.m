#import <Cocoa/Cocoa.h>

extern void goURLEvent(char *url);
extern void goTerminate(void);

@interface CPURLHandler : NSObject <NSApplicationDelegate>
@end

@implementation CPURLHandler
- (void)handle:(NSAppleEventDescriptor *)event withReply:(NSAppleEventDescriptor *)reply {
	NSString *url = [[event paramDescriptorForKeyword:keyDirectObject] stringValue];
	if (url) goURLEvent((char *)[url UTF8String]);
}

// The Dock's Quit ends the app through Cocoa, not a signal: mpv goes with it.
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
	// Cmd+Q on the launcher closes it, which ends the app the normal way
	// (a staged update is applied); anything else ends it here.
	NSWindow *key = NSApp.keyWindow;
	if (key && key.isVisible) {
		[key close];
		return NSTerminateCancel;
	}
	goTerminate();
	return NSTerminateNow;
}
@end

static CPURLHandler *handler;

// runURLLoop registers for "open URL" Apple Events and runs the Cocoa loop on
// the main thread. The app stays out of the Dock: the window is mpv's.
void runURLLoop(void) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		handler = [CPURLHandler new];
		[NSApp setDelegate:handler];
		// The launcher's text field needs the Edit shortcuts (Cmd+V above all), and
		// Cmd+Q must quit: both come from the main menu, which an agent app lacks.
		NSMenu *menu = [NSMenu new], *app = [NSMenu new], *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
		[app addItemWithTitle:@"Quit CanalPeer" action:@selector(terminate:) keyEquivalent:@"q"];
		[edit addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
		[edit addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
		[edit addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
		[edit addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
		[[menu addItemWithTitle:@"" action:nil keyEquivalent:@""] setSubmenu:app];
		[[menu addItemWithTitle:@"Edit" action:nil keyEquivalent:@""] setSubmenu:edit];
		NSApp.mainMenu = menu;
		[[NSAppleEventManager sharedAppleEventManager]
			setEventHandler:handler
			andSelector:@selector(handle:withReply:)
			forEventClass:kInternetEventClass
			andEventID:kAEGetURL];
		[NSApp run];
	}
}
