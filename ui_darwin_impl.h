// Switchboard's native macOS interface (Objective-C, compiled by cgo).
// Go owns all the logic and sends a JSON snapshot; this file only draws it
// and reports button presses back through sbAction(kind, id, payload).

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <ApplicationServices/ApplicationServices.h>

extern void sbAction(char *kind, char *ident, char *payload);

static void SBEmit(NSString *kind, NSString *ident, NSString *payload) {
    sbAction((char *)[kind UTF8String], (char *)[(ident ?: @"") UTF8String], (char *)[(payload ?: @"") UTF8String]);
}

static NSString *SBStr(id v) { return [v isKindOfClass:[NSString class]] ? (NSString *)v : @""; }
static NSArray *SBArr(id v) { return [v isKindOfClass:[NSArray class]] ? (NSArray *)v : @[]; }
static double SBNum(id v) { return [v respondsToSelector:@selector(doubleValue)] ? [v doubleValue] : 0; }

static const CGFloat kCardW = 400, kGap = 10, kMargin = 14;
static BOOL gPopups = YES;

static NSColor *SBRGB(CGFloat r, CGFloat g, CGFloat b, CGFloat a) {
    return [NSColor colorWithSRGBRed:r green:g blue:b alpha:a];
}

// ----- small building blocks -------------------------------------------------

@interface SBPanel : NSPanel
@end
@implementation SBPanel
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return NO; }
@end

@interface SBTarget : NSObject
@property (nonatomic, copy) void (^block)(id sender);
- (void)fire:(id)sender;
@end
@implementation SBTarget
- (void)fire:(id)sender { if (self.block) self.block(sender); }
@end

// A view you can drag around that still reports plain clicks.
@interface SBDragView : NSView
@property (nonatomic, copy) void (^onClick)(void);
@property (nonatomic, copy) void (^onMoved)(NSPoint origin);
@property (nonatomic, copy) void (^onDragging)(void); // fires on every move, so cards can track live
@property (nonatomic, strong) NSMenu *contextMenu;
@end
@implementation SBDragView {
    NSPoint _start, _origin;
    BOOL _moved;
}
- (BOOL)acceptsFirstMouse:(NSEvent *)event { return YES; }
- (NSView *)hitTest:(NSPoint)point { return [super hitTest:point] ? self : nil; }
- (void)mouseDown:(NSEvent *)event {
    _start = [NSEvent mouseLocation];
    _origin = self.window.frame.origin;
    _moved = NO;
}
- (void)mouseDragged:(NSEvent *)event {
    NSPoint p = [NSEvent mouseLocation];
    CGFloat dx = p.x - _start.x, dy = p.y - _start.y;
    if (fabs(dx) + fabs(dy) > 3) _moved = YES;
    [self.window setFrameOrigin:NSMakePoint(_origin.x + dx, _origin.y + dy)];
    if (self.onDragging) self.onDragging();
}
- (void)mouseUp:(NSEvent *)event {
    if (_moved) { if (self.onMoved) self.onMoved(self.window.frame.origin); }
    else if (self.onClick) self.onClick();
}
- (void)rightMouseDown:(NSEvent *)event {
    if (self.contextMenu) [NSMenu popUpContextMenu:self.contextMenu withEvent:event forView:self];
}
@end

@interface SBCard : NSObject
@property (nonatomic, strong) NSPanel *panel;
@property (nonatomic) CGFloat height;
@property (nonatomic) double created;
@property (nonatomic) double expires;
@property (nonatomic, strong) NSTextField *timer;
@property (nonatomic, strong) NSMutableArray *targets;
@property (nonatomic, strong) NSMutableDictionary *chosen;  // question -> NSMutableArray of labels
@property (nonatomic, strong) NSMutableDictionary *groups;  // question -> NSMutableArray of @[button, label]
@end
@implementation SBCard
- (instancetype)init {
    if ((self = [super init])) {
        _targets = [NSMutableArray new];
        _chosen = [NSMutableDictionary new];
        _groups = [NSMutableDictionary new];
    }
    return self;
}
@end

static NSPanel *SBMakePanel(CGFloat w, CGFloat h) {
    SBPanel *p = [[SBPanel alloc] initWithContentRect:NSMakeRect(0, 0, w, h)
                                            styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
                                              backing:NSBackingStoreBuffered
                                                defer:NO];
    p.level = NSStatusWindowLevel;
    p.opaque = NO;
    p.backgroundColor = [NSColor clearColor];
    p.hasShadow = YES;
    p.hidesOnDeactivate = NO;
    p.floatingPanel = YES;
    p.releasedWhenClosed = NO;
    p.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorStationary |
                           NSWindowCollectionBehaviorFullScreenAuxiliary;
    p.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
    NSVisualEffectView *fx = [[NSVisualEffectView alloc] initWithFrame:NSMakeRect(0, 0, w, h)];
    fx.material = NSVisualEffectMaterialHUDWindow;
    fx.blendingMode = NSVisualEffectBlendingModeBehindWindow;
    fx.state = NSVisualEffectStateActive;
    fx.wantsLayer = YES;
    fx.layer.cornerRadius = 14;
    fx.layer.masksToBounds = YES;
    fx.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    p.contentView = fx;
    return p;
}

static NSTextField *SBLabel(NSView *parent, NSString *text, NSRect r, CGFloat size, BOOL bold, BOOL mono,
                            NSColor *color, NSInteger lines) {
    NSTextField *f = [NSTextField labelWithString:(text ?: @"")];
    f.frame = r;
    if (mono) {
        if (@available(macOS 10.15, *)) f.font = [NSFont monospacedSystemFontOfSize:size weight:NSFontWeightRegular];
        else f.font = [NSFont userFixedPitchFontOfSize:size];
    } else {
        f.font = bold ? [NSFont boldSystemFontOfSize:size] : [NSFont systemFontOfSize:size];
    }
    f.textColor = color ?: [NSColor whiteColor];
    if (lines > 1) {
        f.maximumNumberOfLines = lines;
        f.cell.wraps = YES;
        f.cell.truncatesLastVisibleLine = YES;
        f.lineBreakMode = NSLineBreakByWordWrapping;
    } else {
        f.cell.wraps = NO;
        f.lineBreakMode = NSLineBreakByTruncatingTail;
    }
    [parent addSubview:f];
    return f;
}

static NSButton *SBButton(NSView *parent, NSString *title, NSRect r, NSMutableArray *keep,
                          void (^block)(NSButton *b), NSColor *tint) {
    SBTarget *t = [SBTarget new];
    t.block = ^(id sender) { block((NSButton *)sender); };
    [keep addObject:t];  // buttons don't retain their targets
    NSButton *b = [NSButton buttonWithTitle:title target:t action:@selector(fire:)];
    b.frame = r;
    b.bezelStyle = NSBezelStyleRounded;
    if (tint) b.bezelColor = tint;
    [parent addSubview:b];
    return b;
}

// A small round close button. NSBezelStyleRounded clips its title at this
// size (the glyph doesn't render at all below ~24pt), so this uses the
// circular bezel macOS close controls normally use instead.
static NSButton *SBCloseButton(NSView *parent, NSRect r, NSMutableArray *keep, void (^block)(void)) {
    SBTarget *t = [SBTarget new];
    t.block = ^(id sender) { block(); };
    [keep addObject:t];
    NSButton *b = [NSButton buttonWithTitle:@"✕" target:t action:@selector(fire:)];
    b.frame = r;
    b.bezelStyle = NSBezelStyleCircular;
    b.font = [NSFont systemFontOfSize:11];
    [parent addSubview:b];
    return b;
}

static void SBBox(NSView *parent, NSRect r) {
    NSView *v = [[NSView alloc] initWithFrame:r];
    v.wantsLayer = YES;
    v.layer.backgroundColor = [SBRGB(1, 1, 1, 0.08) CGColor];
    v.layer.cornerRadius = 7;
    [parent addSubview:v];
}

static NSInteger SBLineCount(NSString *text, NSInteger perLine, NSInteger cap) {
    NSInteger n = 0;
    for (NSString *line in [(text.length ? text : @" ") componentsSeparatedByString:@"\n"]) {
        n += MAX(1, (NSInteger)ceil((double)line.length / perLine));
    }
    return MAX(1, MIN(cap, n));
}

static NSString *SBClock(double seconds) {
    long s = (long)MAX(0, seconds);
    return [NSString stringWithFormat:@"%ld:%02ld", s / 60, s % 60];
}

static double SBNow(void) { return [[NSDate date] timeIntervalSince1970]; }

// ----- the controller ------------------------------------------------------------

@interface SBController : NSObject <NSMenuDelegate, NSWindowDelegate>
@property (nonatomic, strong) NSStatusItem *statusItem;
@property (nonatomic, strong) NSPanel *widget;
@property (nonatomic, strong) NSTextField *widgetLabel;
@property (nonatomic, strong) SBDragView *widgetView;
@property (nonatomic, strong) NSMutableDictionary<NSString *, SBCard *> *cards;
@property (nonatomic, strong) NSMutableArray<SBCard *> *toasts;
@property (nonatomic, strong) NSDictionary *state;
@property (nonatomic) BOOL widgetVisible;
@property (nonatomic, strong) NSMutableArray *keep;
@property (nonatomic, strong) NSMutableArray *menuTargets;
@property (nonatomic, strong) NSTimer *timer;
@property (nonatomic, strong) NSWindow *dashboardWindow;
@property (nonatomic, strong) WKWebView *dashboardWebView;
@end

@implementation SBController

- (instancetype)init {
    if ((self = [super init])) {
        _cards = [NSMutableDictionary new];
        _toasts = [NSMutableArray new];
        _keep = [NSMutableArray new];
        _menuTargets = [NSMutableArray new];
        _state = @{};
    }
    return self;
}

- (NSRect)screenFrame {
    NSScreen *s = [NSScreen mainScreen] ?: [NSScreen screens].firstObject;
    return s ? s.visibleFrame : NSMakeRect(0, 0, 1440, 900);
}

- (void)setupWithWidgetAt:(NSPoint)pos visible:(BOOL)visible {
    // Menu bar icon (may be hidden by the notch; the floating widget doesn't depend on it).
    self.statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
    self.statusItem.button.title = @"☎︎";
    NSMenu *menu = [NSMenu new];
    menu.delegate = self;
    menu.autoenablesItems = YES;
    self.statusItem.menu = menu;

    // Floating widget.
    self.widget = SBMakePanel(180, 36);
    SBDragView *dv = [[SBDragView alloc] initWithFrame:NSMakeRect(0, 0, 180, 36)];
    dv.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    __weak SBController *ws = self;
    dv.onClick = ^{ [ws widgetClicked]; };
    dv.onMoved = ^(NSPoint o) {
        SBEmit(@"widget_moved", @"", [NSString stringWithFormat:@"%.0f,%.0f", o.x, o.y]);
    };
    dv.onDragging = ^{ [ws layout]; }; // cards stay anchored to the widget while it's being dragged
    NSMenu *ctx = [NSMenu new];
    [self addItem:@"Open dashboard" to:ctx keep:self.keep block:^{ SBEmit(@"dashboard", @"", @""); }];
    [self addItem:@"Hide widget" to:ctx keep:self.keep block:^{ [ws setWidgetShown:NO]; }];
    [ctx addItem:[NSMenuItem separatorItem]];
    [self addItem:@"Quit Switchboard" to:ctx keep:self.keep block:^{ [ws quit]; }];
    dv.contextMenu = ctx;
    [self.widget.contentView addSubview:dv];
    self.widgetView = dv;
    self.widgetLabel = SBLabel(dv, @"☎︎", NSMakeRect(13, 8, 160, 20), 14, NO, NO, nil, 1);

    NSRect vf = [self screenFrame];
    if (pos.x < 0 || pos.y < 0) pos = NSMakePoint(NSMaxX(vf) - 200, NSMinY(vf) + 24);
    [self.widget setFrameOrigin:pos];
    self.widgetVisible = visible;
    if (visible) [self.widget orderFrontRegardless];

    self.timer = [NSTimer scheduledTimerWithTimeInterval:1.0 target:self selector:@selector(tick:) userInfo:nil repeats:YES];
}

- (NSMenuItem *)addItem:(NSString *)title to:(NSMenu *)menu keep:(NSMutableArray *)keep block:(void (^)(void))block {
    NSMenuItem *item;
    if (block) {
        SBTarget *t = [SBTarget new];
        t.block = ^(id sender) { block(); };
        [keep addObject:t];
        item = [[NSMenuItem alloc] initWithTitle:title action:@selector(fire:) keyEquivalent:@""];
        item.target = t;
    } else {
        item = [[NSMenuItem alloc] initWithTitle:title action:nil keyEquivalent:@""];  // header: shown dimmed
    }
    [menu addItem:item];
    return item;
}

- (void)quit {
    SBEmit(@"quit", @"", @"");
    [NSApp terminate:nil];
}

- (void)setWidgetShown:(BOOL)shown {
    self.widgetVisible = shown;
    if (shown) [self.widget orderFrontRegardless];
    else [self.widget orderOut:nil];
}

- (void)widgetClicked {
    if (self.cards.count) {
        for (SBCard *c in self.cards.allValues) [c.panel orderFrontRegardless];
    } else {
        SBEmit(@"dashboard", @"", @"");
    }
}

// Opens the dashboard in its own in-app window (a plain WKWebView) instead
// of the system browser - no browser dependency. Reuses the same window
// across calls instead of making a new one each time.
//
// Switchboard runs with activation policy Accessory (no Dock icon, so the
// floating widget doesn't clutter the Dock/Cmd-Tab). Accessory apps can
// show a window that LOOKS frontmost without actually taking over keyboard
// input - keystrokes keep going to whatever app was really active before,
// which is exactly what made typing into the compact note field leak into
// another window. Fix: promote to a regular app only while this window is
// open, and drop back to Accessory (in windowWillClose:) once it's closed.
- (void)showDashboard:(NSString *)url {
    if (!self.dashboardWindow) {
        NSRect r = NSMakeRect(0, 0, 980, 700);
        NSWindow *w = [[NSWindow alloc] initWithContentRect:r
            styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskResizable | NSWindowStyleMaskMiniaturizable
              backing:NSBackingStoreBuffered defer:NO];
        w.title = @"Switchboard";
        w.releasedWhenClosed = NO;
        w.delegate = self;
        [w center];
        WKWebView *wv = [[WKWebView alloc] initWithFrame:r];
        w.contentView = wv;
        self.dashboardWindow = w;
        self.dashboardWebView = wv;
    }
    NSURL *u = [NSURL URLWithString:url];
    if (u) [self.dashboardWebView loadRequest:[NSURLRequest requestWithURL:u]];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    [NSApp activateIgnoringOtherApps:YES];
    [self.dashboardWindow makeKeyAndOrderFront:nil];
    [self.dashboardWindow makeFirstResponder:self.dashboardWebView];
}

- (void)windowWillClose:(NSNotification *)note {
    if (note.object == self.dashboardWindow) {
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    }
}

// ----- state from Go ------------------------------------------------------------

- (void)apply:(NSString *)json {
    NSData *data = [json dataUsingEncoding:NSUTF8StringEncoding];
    id parsed = data ? [NSJSONSerialization JSONObjectWithData:data options:0 error:nil] : nil;
    if (![parsed isKindOfClass:[NSDictionary class]]) return;
    self.state = parsed;

    NSInteger count = (NSInteger)SBNum(self.state[@"count"]);
    self.statusItem.button.title = count ? [NSString stringWithFormat:@"☎︎ %ld", (long)count] : @"☎︎";

    NSString *text = SBStr(self.state[@"widget_text"]);
    if (text.length && ![text isEqualToString:self.widgetLabel.stringValue]) {
        self.widgetLabel.stringValue = text;
        [self.widgetLabel sizeToFit];
        CGFloat w = MAX(70, self.widgetLabel.frame.size.width + 26);
        NSRect f = self.widget.frame;
        [self.widget setFrame:NSMakeRect(f.origin.x, f.origin.y, w, 36) display:YES];
        [self.widgetLabel setFrameOrigin:NSMakePoint(13, 8)];
    }
    if (gPopups) [self syncCards];
}

- (void)syncCards {
    NSArray *pending = SBArr(self.state[@"pending"]);
    NSMutableSet *ids = [NSMutableSet new];
    BOOL changed = NO, fresh = NO;
    for (NSDictionary *p in pending) {
        if (![p isKindOfClass:[NSDictionary class]]) continue;
        NSString *pid = SBStr(p[@"id"]);
        [ids addObject:pid];
        if (!self.cards[pid]) {
            SBCard *c = [SBStr(p[@"kind"]) isEqualToString:@"question"] ? [self questionCard:p] : [self permissionCard:p];
            self.cards[pid] = c;
            changed = fresh = YES;
        }
    }
    for (NSString *pid in self.cards.allKeys) {
        if (![ids containsObject:pid]) {
            [self.cards[pid].panel orderOut:nil];
            [self.cards removeObjectForKey:pid];
            changed = YES;
        }
    }
    if (changed) [self layout];
    if (fresh) [[NSSound soundNamed:@"Glass"] play];
}

- (void)closeCard:(NSString *)pid {
    SBCard *c = self.cards[pid];
    if (!c) return;
    [c.panel orderOut:nil];
    [self.cards removeObjectForKey:pid];
    [self layout];
}

// Cards stack next to wherever the widget currently is, growing away from
// the nearer screen edge (up if the widget's in the bottom half, down if
// it's in the top half) so they don't run off-screen.
- (void)layout {
    NSRect vf = [self screenFrame];
    NSRect wf = self.widget.frame;
    BOOL growUp = NSMidY(wf) < NSMidY(vf);
    CGFloat x = wf.origin.x;
    if (x + kCardW > NSMaxX(vf)) x = NSMaxX(vf) - kCardW - 16;
    if (x < NSMinX(vf)) x = NSMinX(vf) + 16;
    CGFloat anchor = growUp ? NSMaxY(wf) + kGap : NSMinY(wf) - kGap;

    NSArray *sorted = [self.cards.allValues sortedArrayUsingComparator:^NSComparisonResult(SBCard *a, SBCard *b) {
        return a.created < b.created ? NSOrderedAscending : (a.created > b.created ? NSOrderedDescending : NSOrderedSame);
    }];
    NSArray *all = [sorted arrayByAddingObjectsFromArray:self.toasts];
    NSInteger shown = 0;
    for (SBCard *c in all) {
        CGFloat y = growUp ? anchor : anchor - c.height;
        if (shown >= 6 || y < NSMinY(vf) || y + c.height > NSMaxY(vf)) { [c.panel orderOut:nil]; continue; }
        [c.panel setFrameOrigin:NSMakePoint(x, y)];
        [c.panel orderFrontRegardless];
        anchor = growUp ? y + c.height + kGap : y - kGap;
        shown++;
    }
}

- (void)tick:(NSTimer *)t {
    double now = SBNow();
    for (SBCard *c in self.cards.allValues) {
        if (c.timer && c.created > 0) c.timer.stringValue = SBClock(now - c.created);
    }
    BOOL changed = NO;
    for (SBCard *c in [self.toasts copy]) {
        if (c.expires > 0 && now > c.expires) {
            [c.panel orderOut:nil];
            [self.toasts removeObject:c];
            changed = YES;
        }
    }
    if (changed) [self layout];
}

// ----- cards ---------------------------------------------------------------------

- (SBCard *)permissionCard:(NSDictionary *)p {
    NSString *code = SBStr(p[@"code"]);
    CGFloat codeH = SBLineCount(code, 50, 6) * 15 + 14;
    CGFloat h = kMargin + 20 + 2 + 18 + 10 + codeH + 12 + 28 + 8 + 28 + kMargin;
    SBCard *c = [SBCard new];
    c.panel = SBMakePanel(kCardW, h);
    c.height = h;
    c.created = SBNum(p[@"created"]);
    NSView *v = c.panel.contentView;
    NSString *pid = SBStr(p[@"id"]), *sid = SBStr(p[@"session_id"]);
    __weak SBController *ws = self;

    CGFloat y = h - kMargin - 20;
    SBLabel(v, @"●", NSMakeRect(14, y, 14, 20), 13, NO, NO, SBRGB(0.95, 0.68, 0.16, 1), 1);
    SBLabel(v, SBStr(p[@"project"]), NSMakeRect(30, y, kCardW - 132, 20), 14, YES, NO, nil, 1);
    c.timer = SBLabel(v, @"0:00", NSMakeRect(kCardW - 96, y, 56, 20), 12, NO, NO, SBRGB(1, 1, 1, 0.62), 1);
    c.timer.alignment = NSTextAlignmentRight;
    SBCloseButton(v, NSMakeRect(kCardW - 36, y - 2, 24, 24), c.targets, ^{
        [ws closeCard:pid]; // dismiss only: no SBEmit, so Claude keeps waiting and the card reappears on the next hook event
    });
    y -= 2 + 18;
    SBLabel(v, SBStr(p[@"verb"]), NSMakeRect(30, y, kCardW - 44, 18), 12, NO, NO, SBRGB(1, 1, 1, 0.62), 1);
    y -= 10 + codeH;
    SBBox(v, NSMakeRect(14, y, kCardW - 28, codeH));
    SBLabel(v, code, NSMakeRect(22, y + 7, kCardW - 44, codeH - 14), 11.5, NO, YES, nil, 6);
    y -= 12 + 28;
    SBButton(v, @"Allow", NSMakeRect(12, y, 92, 28), c.targets, ^(NSButton *b) {
        SBEmit(@"allow", pid, @"");
        [ws closeCard:pid];
    }, SBRGB(0.20, 0.62, 0.36, 1));
    SBButton(v, @"Deny", NSMakeRect(104, y, 84, 28), c.targets, ^(NSButton *b) {
        SBEmit(@"deny", pid, @"");
        [ws closeCard:pid];
    }, nil);
    SBButton(v, @"Open in terminal", NSMakeRect(kCardW - 150, y, 140, 28), c.targets, ^(NSButton *b) {
        SBEmit(@"open_terminal", pid, sid);
        [ws closeCard:pid];
    }, nil);
    y -= 8 + 28;
    SBButton(v, @"Always allow this", NSMakeRect(12, y, kCardW - 24, 28), c.targets, ^(NSButton *b) {
        SBEmit(@"always_allow", pid, @"");
        [ws closeCard:pid];
    }, nil);
    return c;
}

- (SBCard *)questionCard:(NSDictionary *)p {
    NSArray *qs = SBArr(p[@"questions"]);
    BOOL instant = qs.count == 1 && ![qs[0][@"multi"] boolValue];  // one click answers
    CGFloat h = kMargin + 20 + 8;
    for (NSDictionary *q in qs) h += 36 + 32 * MIN((NSInteger)SBArr(q[@"options"]).count, 6) + 6;
    h += 8 + 28 + kMargin;

    SBCard *c = [SBCard new];
    c.panel = SBMakePanel(kCardW, h);
    c.height = h;
    c.created = SBNum(p[@"created"]);
    NSView *v = c.panel.contentView;
    NSString *pid = SBStr(p[@"id"]), *sid = SBStr(p[@"session_id"]);
    __weak SBController *ws = self;
    __weak SBCard *wc = c;

    CGFloat y = h - kMargin - 20;
    SBLabel(v, @"●", NSMakeRect(14, y, 14, 20), 13, NO, NO, SBRGB(0.95, 0.68, 0.16, 1), 1);
    SBLabel(v, [NSString stringWithFormat:@"%@ has a question", SBStr(p[@"project"])],
            NSMakeRect(30, y, kCardW - 132, 20), 14, YES, NO, nil, 1);
    c.timer = SBLabel(v, @"0:00", NSMakeRect(kCardW - 96, y, 56, 20), 12, NO, NO, SBRGB(1, 1, 1, 0.62), 1);
    c.timer.alignment = NSTextAlignmentRight;
    SBCloseButton(v, NSMakeRect(kCardW - 36, y - 2, 24, 24), c.targets, ^{
        [ws closeCard:pid]; // dismiss only: no SBEmit, so Claude keeps waiting and the card reappears on the next hook event
    });
    y -= 8;

    for (NSDictionary *q in qs) {
        NSString *question = SBStr(q[@"question"]);
        BOOL multi = [q[@"multi"] boolValue];
        y -= 36;
        SBLabel(v, multi ? [question stringByAppendingString:@"  (pick any)"] : question,
                NSMakeRect(14, y, kCardW - 28, 34), 12.5, NO, NO, nil, 2);
        NSMutableArray *group = [NSMutableArray new];
        c.groups[question] = group;
        NSArray *opts = SBArr(q[@"options"]);
        for (NSUInteger i = 0; i < MIN(opts.count, 6); i++) {
            NSDictionary *o = opts[i];
            NSString *label = SBStr(o[@"label"]);
            y -= 32;
            NSButton *b = SBButton(v, label, NSMakeRect(12, y, kCardW - 24, 28), c.targets, ^(NSButton *btn) {
                if (instant) {
                    NSData *d = [NSJSONSerialization dataWithJSONObject:@{question : label} options:0 error:nil];
                    SBEmit(@"answer", pid, [[NSString alloc] initWithData:d encoding:NSUTF8StringEncoding]);
                    [ws closeCard:pid];
                    return;
                }
                SBCard *card = wc;
                NSMutableArray *sel = card.chosen[question] ?: [NSMutableArray new];
                if (multi) {
                    if ([sel containsObject:label]) [sel removeObject:label];
                    else [sel addObject:label];
                } else {
                    [sel removeAllObjects];
                    [sel addObject:label];
                }
                card.chosen[question] = sel;
                for (NSArray *pair in card.groups[question]) {
                    ((NSButton *)pair[0]).state = [sel containsObject:pair[1]] ? NSControlStateValueOn : NSControlStateValueOff;
                }
            }, nil);
            if (!instant) [b setButtonType:NSButtonTypePushOnPushOff];
            NSString *desc = SBStr(o[@"description"]);
            if (desc.length) b.toolTip = desc;
            [group addObject:@[ b, label ]];
        }
        y -= 6;
    }

    y -= 8 + 28;
    if (!instant) {
        SBButton(v, @"Send answers", NSMakeRect(12, y, 140, 28), c.targets, ^(NSButton *b) {
            SBCard *card = wc;
            NSMutableDictionary *answers = [NSMutableDictionary new];
            for (NSDictionary *q in qs) {
                NSString *question = SBStr(q[@"question"]);
                NSArray *sel = card.chosen[question];
                if (!sel.count) { NSBeep(); return; }
                answers[question] = sel.count == 1 ? sel[0] : sel;
            }
            NSData *d = [NSJSONSerialization dataWithJSONObject:answers options:0 error:nil];
            SBEmit(@"answer", pid, [[NSString alloc] initWithData:d encoding:NSUTF8StringEncoding]);
            [ws closeCard:pid];
        }, SBRGB(0.20, 0.62, 0.36, 1));
    }
    SBButton(v, @"Type my own in terminal", NSMakeRect(kCardW - 200, y, 190, 28), c.targets, ^(NSButton *b) {
        SBEmit(@"open_terminal", pid, sid);
        [ws closeCard:pid];
    }, nil);
    return c;
}

- (void)toastTitle:(NSString *)title body:(NSString *)body session:(NSString *)sid {
    if (!gPopups) return;
    NSInteger lines = body.length > 52 ? 2 : 1;
    CGFloat h = kMargin + 20 + 4 + 16 * lines + 10 + 28 + kMargin;
    SBCard *c = [SBCard new];
    c.panel = SBMakePanel(kCardW, h);
    c.height = h;
    c.expires = SBNow() + 7;
    NSView *v = c.panel.contentView;
    __weak SBController *ws = self;
    __weak SBCard *wc = c;
    void (^dismiss)(void) = ^{
        SBController *s = ws;
        SBCard *card = wc;
        if (!s || !card) return;
        [card.panel orderOut:nil];
        [s.toasts removeObject:card];
        [s layout];
    };
    CGFloat y = h - kMargin - 20;
    SBLabel(v, title, NSMakeRect(14, y, kCardW - 28, 20), 13, YES, NO, nil, 1);
    y -= 4 + 16 * lines;
    SBLabel(v, body, NSMakeRect(14, y, kCardW - 28, 16 * lines), 12, NO, NO, SBRGB(1, 1, 1, 0.62), lines);
    y -= 10 + 28;
    if (sid.length) {
        SBButton(v, @"Open terminal", NSMakeRect(12, y, 130, 28), c.targets, ^(NSButton *b) {
            SBEmit(@"show", sid, @"");
            dismiss();
        }, nil);
    }
    SBButton(v, @"Dismiss", NSMakeRect(kCardW - 100, y, 90, 28), c.targets, ^(NSButton *b) { dismiss(); }, nil);
    [self.toasts addObject:c];
    [self layout];
}

// ----- menu bar menu, rebuilt each time it opens -----------------------------------

- (void)menuNeedsUpdate:(NSMenu *)menu {
    [menu removeAllItems];
    [self.menuTargets removeAllObjects];
    NSMutableArray *keep = self.menuTargets;
    NSArray *pending = SBArr(self.state[@"pending"]);
    if (pending.count) {
        [self addItem:@"Waiting for you" to:menu keep:keep block:nil];
        for (NSDictionary *p in pending) {
            NSString *pid = SBStr(p[@"id"]), *sid = SBStr(p[@"session_id"]);
            BOOL question = [SBStr(p[@"kind"]) isEqualToString:@"question"];
            NSString *what = question ? @"has a question" : SBStr(p[@"verb"]);
            NSMenuItem *parent = [self addItem:[NSString stringWithFormat:@"🟠 %@ %@", SBStr(p[@"project"]), what]
                                            to:menu keep:keep block:nil];
            NSMenu *sub = [NSMenu new];
            if (question) {
                NSArray *qs = SBArr(p[@"questions"]);
                if (qs.count == 1 && ![qs[0][@"multi"] boolValue]) {
                    NSString *question = SBStr(qs[0][@"question"]);
                    [self addItem:question to:sub keep:keep block:nil];
                    for (NSDictionary *o in SBArr(qs[0][@"options"])) {
                        NSString *label = SBStr(o[@"label"]);
                        [self addItem:label to:sub keep:keep block:^{
                            NSData *d = [NSJSONSerialization dataWithJSONObject:@{question : label} options:0 error:nil];
                            SBEmit(@"answer", pid, [[NSString alloc] initWithData:d encoding:NSUTF8StringEncoding]);
                        }];
                    }
                } else {
                    [self addItem:@"Answer in dashboard" to:sub keep:keep block:^{ SBEmit(@"dashboard", @"", @""); }];
                }
            } else {
                [self addItem:@"Allow" to:sub keep:keep block:^{ SBEmit(@"allow", pid, @""); }];
                [self addItem:@"Deny" to:sub keep:keep block:^{ SBEmit(@"deny", pid, @""); }];
            }
            [self addItem:@"Open in terminal" to:sub keep:keep block:^{ SBEmit(@"open_terminal", pid, sid); }];
            parent.submenu = sub;
        }
        [menu addItem:[NSMenuItem separatorItem]];
    }
    [self addItem:@"Sessions" to:menu keep:keep block:nil];
    NSArray *sessions = SBArr(self.state[@"sessions"]);
    NSDictionary *dots = @{@"needs_permission" : @"🟠", @"needs_input" : @"🟠", @"working" : @"🟢", @"idle" : @"⚪️", @"error" : @"🔴"};
    NSDictionary *labels = @{@"needs_permission" : @"needs permission", @"needs_input" : @"waiting for you",
                             @"working" : @"working", @"idle" : @"your turn", @"error" : @"error"};
    if (!sessions.count) [self addItem:@"No sessions yet. Start claude in any terminal." to:menu keep:keep block:nil];
    for (NSDictionary *s in sessions) {
        NSString *sid = SBStr(s[@"id"]), *status = SBStr(s[@"status"]);
        NSString *title = [NSString stringWithFormat:@"%@ %@, %@", dots[status] ?: @"⚪️", SBStr(s[@"project"]), labels[status] ?: status];
        [self addItem:title to:menu keep:keep block:^{ SBEmit(@"show", sid, @""); }];
    }
    [menu addItem:[NSMenuItem separatorItem]];
    __weak SBController *ws = self;
    BOOL shown = self.widgetVisible;
    [self addItem:(shown ? @"Hide floating widget" : @"Show floating widget") to:menu keep:keep block:^{ [ws setWidgetShown:!shown]; }];
    [self addItem:@"Open dashboard" to:menu keep:keep block:^{ SBEmit(@"dashboard", @"", @""); }];
    [self addItem:@"Quit Switchboard" to:menu keep:keep block:^{ [ws quit]; }];
}

@end

// ----- entry points called from Go ------------------------------------------------

static SBController *gController = nil;

static void SBRun(const char *initial, double wx, double wy, int showWidget, int popups) {
    @autoreleasepool {
        gPopups = popups != 0;
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];  // no Dock icon
        gController = [SBController new];
        [gController setupWithWidgetAt:NSMakePoint(wx, wy) visible:(showWidget != 0)];
        if (initial) [gController apply:[NSString stringWithUTF8String:initial]];
        [NSApp run];
    }
}

static void SBUpdate(const char *json) {
    NSString *s = [NSString stringWithUTF8String:json];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (gController && s) [gController apply:s];
    });
}

static void SBToast(const char *title, const char *body, const char *sid) {
    NSString *t = [NSString stringWithUTF8String:title], *b = [NSString stringWithUTF8String:body],
             *i = [NSString stringWithUTF8String:sid];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (gController) [gController toastTitle:t body:b session:i];
    });
}

static void SBShowDashboard(const char *url) {
    NSString *u = [NSString stringWithUTF8String:url];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (gController) [gController showDashboard:u];
    });
}

// Synthesizes real keyboard events via Core Graphics (the same layer
// System Events/osascript itself calls into) instead of shelling out to
// osascript - so the Accessibility permission this requires is granted to
// switchboard specifically, not the shared system AppleScript interpreter
// every app and script on the Mac can invoke. Runs synchronously off the
// main thread: CGEventPost and NSRunningApplication activation don't need
// it, unlike the AppKit window code elsewhere in this file.
static int SBSendKeystrokes(const char *bundleID, const char *text) {
    NSDictionary *opts = @{(__bridge id)kAXTrustedCheckOptionPrompt : @YES};
    if (!AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)opts)) return 0;

    NSString *bid = [NSString stringWithUTF8String:bundleID];
    NSArray<NSRunningApplication *> *apps = [NSRunningApplication runningApplicationsWithBundleIdentifier:bid];
    NSRunningApplication *app = apps.firstObject;
    if (!app) return 0;
    [app activateWithOptions:NSApplicationActivateIgnoringOtherApps];
    [NSThread sleepForTimeInterval:0.3];

    NSString *s = [NSString stringWithUTF8String:text];
    NSUInteger len = s.length;
    unichar *buf = malloc(sizeof(unichar) * MAX(len, 1));
    [s getCharacters:buf range:NSMakeRange(0, len)];
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
    CGEventKeyboardSetUnicodeString(down, len, buf);
    CGEventPost(kCGHIDEventTap, down);
    CFRelease(down);
    CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
    CGEventKeyboardSetUnicodeString(up, len, buf);
    CGEventPost(kCGHIDEventTap, up);
    CFRelease(up);
    free(buf);

    [NSThread sleepForTimeInterval:0.2];
    CGEventRef rd = CGEventCreateKeyboardEvent(NULL, 36, true); // Return
    CGEventPost(kCGHIDEventTap, rd);
    CFRelease(rd);
    CGEventRef ru = CGEventCreateKeyboardEvent(NULL, 36, false);
    CGEventPost(kCGHIDEventTap, ru);
    CFRelease(ru);
    return 1;
}
