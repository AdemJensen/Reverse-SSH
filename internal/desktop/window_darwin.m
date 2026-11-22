#import <Cocoa/Cocoa.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#import <WebKit/WebKit.h>
#include <stdbool.h>
#include <string.h>

static NSString *const AppTitle = @"SSH 反向隧道";
static NSString *const ReopenNotification = @"com.reversessh.desktop.reopen";
static NSWindow *window;
static NSStatusItem *statusItem;
static NSSavePanel *activePanel;
enum { PickKeyFile, PickImportFile, PickExportFile };

@interface TunnelWindowDelegate : NSObject<NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate>
@property (copy) NSURL *entryURL;
- (void)showWindow:(id)sender;
- (void)quit:(id)sender;
@end

@implementation TunnelWindowDelegate
- (void)createWindow {
    NSUInteger style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                       NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    NSSize contentSize = NSMakeSize(1080, 625);
    NSScreen *screen = NSScreen.mainScreen;
    if (screen) {
        NSSize available = [NSWindow contentRectForFrameRect:screen.visibleFrame styleMask:style].size;
        contentSize.width = MIN(contentSize.width, available.width);
        contentSize.height = MIN(contentSize.height, available.height);
    }
    NSRect contentRect = NSMakeRect(0, 0, contentSize.width, contentSize.height);
    window = [[NSWindow alloc] initWithContentRect:contentRect styleMask:style
                                           backing:NSBackingStoreBuffered defer:NO];
    window.releasedWhenClosed = NO;
    window.title = AppTitle;
    window.contentMinSize = NSMakeSize(MIN(704, contentSize.width), MIN(580, contentSize.height));
    window.delegate = self;
    [window center];

    WKWebViewConfiguration *config = [[[WKWebViewConfiguration alloc] init] autorelease];
    config.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];
    config.preferences.javaScriptCanOpenWindowsAutomatically = NO;
    WKWebView *view = [[[WKWebView alloc] initWithFrame:contentRect configuration:config] autorelease];
    view.navigationDelegate = self;
    window.contentView = view;
    [view loadRequest:[NSURLRequest requestWithURL:self.entryURL]];
}

- (BOOL)windowShouldClose:(NSWindow *)sender {
    [sender orderOut:nil];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    return NO;
}

- (void)showWindow:(id)sender {
    [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    if (!window) {
        [self createWindow];
    }
    if (window.isMiniaturized) {
        [window deminiaturize:nil];
    }
    [window makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)visible {
    [self showWindow:nil];
    return NO;
}

- (void)quit:(id)sender {
    if (activePanel) {
        [activePanel cancel:nil];
        [self performSelector:@selector(quit:) withObject:nil afterDelay:0 inModes:@[NSDefaultRunLoopMode]];
        return;
    }
    [NSApp stop:nil];
    NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
                                      location:NSZeroPoint modifierFlags:0 timestamp:0
                                  windowNumber:0 context:nil subtype:0 data1:0 data2:0];
    [NSApp postEvent:event atStart:YES];
}

- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action
    decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
    BOOL allowed = action.targetFrame.isMainFrame && [action.request.URL isEqual:self.entryURL];
    decisionHandler(allowed ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}

- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation
      withError:(NSError *)error {
    if (error.code == NSURLErrorCancelled) {
        return;
    }
    NSAlert *alert = [[[NSAlert alloc] init] autorelease];
    alert.messageText = @"无法载入本机界面";
    alert.informativeText = error.localizedDescription;
    [alert addButtonWithTitle:@"退出"];
    [alert beginSheetModalForWindow:window completionHandler:^(NSModalResponse response) {
        [self quit:nil];
    }];
}
@end

static void install_menu(TunnelWindowDelegate *delegate) {
    NSMenu *menu = [[[NSMenu alloc] init] autorelease];
    NSMenuItem *app = [[[NSMenuItem alloc] init] autorelease];
    [menu addItem:app];
    NSMenu *appMenu = [[[NSMenu alloc] initWithTitle:AppTitle] autorelease];
    NSMenuItem *quit = [appMenu addItemWithTitle:@"退出 SSH 反向隧道" action:@selector(quit:) keyEquivalent:@"q"];
    quit.target = delegate;
    app.submenu = appMenu;

    NSMenuItem *edit = [[[NSMenuItem alloc] init] autorelease];
    NSMenu *editMenu = [[[NSMenu alloc] initWithTitle:@"编辑"] autorelease];
    [editMenu addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
    [editMenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
    [editMenu addItemWithTitle:@"复制" action:@selector(copy:) keyEquivalent:@"c"];
    [editMenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
    [editMenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
    edit.submenu = editMenu;
    [menu addItem:edit];

    NSMenuItem *windowItem = [[[NSMenuItem alloc] init] autorelease];
    NSMenu *windowMenu = [[[NSMenu alloc] initWithTitle:@"窗口"] autorelease];
    [windowMenu addItemWithTitle:@"关闭窗口" action:@selector(performClose:) keyEquivalent:@"w"];
    windowItem.submenu = windowMenu;
    [menu addItem:windowItem];
    NSApp.mainMenu = menu;
}

static void install_status_item(TunnelWindowDelegate *delegate) {
    statusItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength] retain];
    NSImage *image = [NSImage imageWithSystemSymbolName:@"arrow.left.arrow.right" accessibilityDescription:AppTitle];
    if (image) {
        image.template = YES;
        statusItem.button.image = image;
    } else {
        statusItem.button.title = @"SSH";
    }
    statusItem.button.toolTip = AppTitle;

    NSMenu *menu = [[[NSMenu alloc] initWithTitle:AppTitle] autorelease];
    NSMenuItem *show = [menu addItemWithTitle:@"打开窗口" action:@selector(showWindow:) keyEquivalent:@""];
    show.target = delegate;
    [menu addItem:NSMenuItem.separatorItem];
    NSMenuItem *quit = [menu addItemWithTitle:@"退出 SSH 反向隧道" action:@selector(quit:) keyEquivalent:@""];
    quit.target = delegate;
    statusItem.menu = menu;
}

void run_window(const char *url, bool start_hidden) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:start_hidden ? NSApplicationActivationPolicyAccessory : NSApplicationActivationPolicyRegular];
        TunnelWindowDelegate *delegate = [[[TunnelWindowDelegate alloc] init] autorelease];
        NSApp.delegate = delegate;
        delegate.entryURL = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
        install_menu(delegate);
        install_status_item(delegate);

        NSDistributedNotificationCenter *notifications = [NSDistributedNotificationCenter defaultCenter];
        [notifications addObserver:delegate selector:@selector(showWindow:) name:ReopenNotification object:nil
                  suspensionBehavior:NSNotificationSuspensionBehaviorDeliverImmediately];
        if (!start_hidden) {
            [delegate showWindow:nil];
        }
        [NSApp run];
        [notifications removeObserver:delegate];
        [[NSStatusBar systemStatusBar] removeStatusItem:statusItem];
        [statusItem release];
        statusItem = nil;
        NSApp.delegate = nil;
        [window release];
        window = nil;
    }
}

void alert_message(const char *message) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        [NSApp activateIgnoringOtherApps:YES];
        NSAlert *alert = [[[NSAlert alloc] init] autorelease];
        alert.messageText = AppTitle;
        alert.informativeText = [NSString stringWithUTF8String:message] ?: @"无法启动应用";
        [alert addButtonWithTitle:@"确定"];
        [alert runModal];
    }
}

void reopen_existing(void) {
    @autoreleasepool {
        [[NSDistributedNotificationCenter defaultCenter] postNotificationName:ReopenNotification
                                                                        object:nil userInfo:nil deliverImmediately:YES];
    }
}

char *choose_file(const char *start_dir, const char *suggested_name, int kind) {
    __block char *selected = NULL;
    void (^choose)(void) = ^{
        @autoreleasepool {
            if (activePanel) {
                return;
            }
            NSSavePanel *panel;
            if (kind == PickExportFile) {
                panel = [NSSavePanel savePanel];
                panel.title = @"导出连接配置";
                panel.prompt = @"保存";
                panel.nameFieldStringValue = [NSString stringWithUTF8String:suggested_name];
            } else {
                NSOpenPanel *open = [NSOpenPanel openPanel];
                open.title = kind == PickImportFile ? @"导入连接配置" : @"选择 SSH 私钥";
                open.prompt = kind == PickImportFile ? @"导入" : @"选择";
                open.canChooseDirectories = NO;
                open.allowsMultipleSelection = NO;
                open.showsHiddenFiles = kind == PickKeyFile;
                panel = open;
            }
            if (kind != PickKeyFile) {
                panel.allowedContentTypes = @[UTTypeJSON];
                panel.allowsOtherFileTypes = NO;
            }
            NSString *dir = [NSString stringWithUTF8String:start_dir];
            if (!dir.length && kind == PickKeyFile) {
                dir = [NSHomeDirectory() stringByAppendingPathComponent:@".ssh"];
            }
            BOOL isDirectory = NO;
            if ([[NSFileManager defaultManager] fileExistsAtPath:dir isDirectory:&isDirectory] && isDirectory) {
                panel.directoryURL = [NSURL fileURLWithPath:dir isDirectory:YES];
            }
            activePanel = panel;
            if ([panel runModal] == NSModalResponseOK && panel.URL.fileSystemRepresentation) {
                selected = strdup(panel.URL.fileSystemRepresentation);
            }
            activePanel = nil;
        }
    };
    if ([NSThread isMainThread]) {
        choose();
    } else {
        dispatch_sync(dispatch_get_main_queue(), choose);
    }
    return selected;
}
