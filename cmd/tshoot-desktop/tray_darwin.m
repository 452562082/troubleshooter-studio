//go:build darwin
// +build darwin

#import <Cocoa/Cocoa.h>

extern void tshootTrayOpen(void);
extern void tshootTrayQuit(void);

@interface TshootStudioTrayTarget : NSObject
@end

@implementation TshootStudioTrayTarget
- (void)openStudio:(id)sender {
    tshootTrayOpen();
}

- (void)quitStudio:(id)sender {
    tshootTrayQuit();
}
@end

static NSStatusItem *tshootStatusItem;
static TshootStudioTrayTarget *tshootTrayTarget;

void tshootStartTray(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (tshootStatusItem != nil) {
            return;
        }

        tshootTrayTarget = [TshootStudioTrayTarget new];
        tshootStatusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:28.0];
        if ([tshootStatusItem respondsToSelector:@selector(setAutosaveName:)]) {
            [tshootStatusItem setAutosaveName:@"studio.troubleshooter.desktop.statusItem"];
        }
        NSStatusBarButton *button = [tshootStatusItem button];
        [button setToolTip:@"Troubleshooter Studio"];

        if (@available(macOS 11.0, *)) {
            NSImage *icon = [NSImage imageWithSystemSymbolName:@"wrench.and.screwdriver" accessibilityDescription:@"Troubleshooter Studio"];
            [icon setTemplate:YES];
            [icon setSize:NSMakeSize(18, 18)];
            [button setImage:icon];
        } else {
            [button setTitle:@"TS"];
        }

        NSMenu *trayMenu = [[NSMenu alloc] initWithTitle:@"Troubleshooter Studio"];

        NSMenuItem *openItem = [[NSMenuItem alloc] initWithTitle:@"打开工作台" action:@selector(openStudio:) keyEquivalent:@""];
        [openItem setTarget:tshootTrayTarget];
        [trayMenu addItem:openItem];

        [trayMenu addItem:[NSMenuItem separatorItem]];

        NSMenuItem *quitItem = [[NSMenuItem alloc] initWithTitle:@"退出" action:@selector(quitStudio:) keyEquivalent:@""];
        [quitItem setTarget:tshootTrayTarget];
        [trayMenu addItem:quitItem];

        [tshootStatusItem setMenu:trayMenu];
    });
}
