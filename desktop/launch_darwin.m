#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

extern char *goLauncherMessage(char *fn, char *arg);
extern void goLauncherClosed(void);

// The page calls these like the WebView2 bindings on Windows.
static NSString *const bridge =
	@"window.cpWatch = t => new Promise(r => { window._cpResolve = r;"
	@"  webkit.messageHandlers.cp.postMessage({fn: 'watch', arg: t}); });"
	@"window.cpDownload = () => webkit.messageHandlers.cp.postMessage({fn: 'download', arg: ''});"
	@"window.cpLicenses = () => webkit.messageHandlers.cp.postMessage({fn: 'licenses', arg: ''});"
	@"window.cpForget = id => webkit.messageHandlers.cp.postMessage({fn: 'forget', arg: id});"
	@"window.cpSetVpn = m => webkit.messageHandlers.cp.postMessage({fn: 'setVpn', arg: m});";

@interface CPLauncher : NSObject <WKScriptMessageHandler, NSWindowDelegate>
@property(strong) NSWindow *window;
@property(strong) WKWebView *web;
@end

@implementation CPLauncher
- (void)userContentController:(WKUserContentController *)ucc didReceiveScriptMessage:(WKScriptMessage *)msg {
	NSDictionary *body = msg.body;
	char *reply = goLauncherMessage((char *)[body[@"fn"] UTF8String], (char *)[body[@"arg"] UTF8String]);
	if ([body[@"fn"] isEqualToString:@"watch"]) {
		NSData *json = [NSJSONSerialization dataWithJSONObject:@[[NSString stringWithUTF8String:reply]] options:0 error:nil];
		NSString *arg = [[NSString alloc] initWithData:json encoding:NSUTF8StringEncoding];
		[self.web evaluateJavaScript:[NSString stringWithFormat:@"window._cpResolve(%@[0])", arg] completionHandler:nil];
	}
	free(reply);
}

- (void)windowWillClose:(NSNotification *)n {
	// Out of the Dock again: while a channel plays, the player is the app's only icon.
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	goLauncherClosed();
}
@end

static CPLauncher *launcher;

void cpShowLauncher(char *html) {
	NSString *page = [NSString stringWithUTF8String:html];
	free(html);
	dispatch_async(dispatch_get_main_queue(), ^{
		launcher = [CPLauncher new];
		WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
		[cfg.userContentController addScriptMessageHandler:launcher name:@"cp"];
		[cfg.userContentController addUserScript:[[WKUserScript alloc] initWithSource:bridge
			injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:YES]];
		NSRect frame = NSMakeRect(0, 0, 480, 680);
		launcher.web = [[WKWebView alloc] initWithFrame:frame configuration:cfg];
		launcher.window = [[NSWindow alloc] initWithContentRect:frame
			styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable
			backing:NSBackingStoreBuffered defer:NO];
		launcher.window.title = @"CanalPeer";
		launcher.window.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
		launcher.window.releasedWhenClosed = NO;
		launcher.window.backgroundColor = [NSColor colorWithRed:0x0f/255.0 green:0x11/255.0 blue:0x15/255.0 alpha:1];
		launcher.window.contentView = launcher.web;
		launcher.window.delegate = launcher;
		[launcher.web loadHTMLString:page baseURL:nil];
		[launcher.window center];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		[launcher.window makeKeyAndOrderFront:nil];
		[NSApp activateIgnoringOtherApps:YES];
	});
}

void cpCloseLauncher(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[launcher.window close];
	});
}



