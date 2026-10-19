// background.swift - Draw the DMG window's background
//
// Usage: swift build/darwin/dmg/background.swift <out-dir>
//
// Writes background.png (1x) and background@2x.png to <out-dir>; make-dmg.sh
// combines them into the background.tiff it lays the window out on. The
// background is committed, so this only needs re-running to change it.
//
// The layout has to agree with dmg-settings.py: the window is WIDTH x HEIGHT
// points, and the app and the Applications link sit at (APP_X, ICON_Y) and
// (APPS_X, ICON_Y), the arrow drawn between them.

import AppKit

let width: CGFloat = 660
let height: CGFloat = 380
let appX: CGFloat = 180
let appsX: CGFloat = 480
let iconY: CGFloat = 205
let iconSize: CGFloat = 128

let background = NSColor(srgbRed: 0.961, green: 0.949, blue: 0.918, alpha: 1)  // #F5F2EA
let titleColor = NSColor(srgbRed: 0.149, green: 0.141, blue: 0.122, alpha: 1)  // #26241F
let subtitleColor = NSColor(srgbRed: 0.490, green: 0.471, blue: 0.424, alpha: 1)  // #7D786C
let arrowColor = NSColor(srgbRed: 0.306, green: 0.604, blue: 0.165, alpha: 1)  // #4E9A2A, the icon's green

// draw renders the background at scale, in points measured from the top left
// as Finder places icons.
func draw(scale: CGFloat) -> Data {
  let rep = NSBitmapImageRep(
    bitmapDataPlanes: nil, pixelsWide: Int(width * scale), pixelsHigh: Int(height * scale),
    bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
    colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
  rep.size = NSSize(width: width, height: height)

  NSGraphicsContext.saveGraphicsState()
  NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)

  background.setFill()
  NSRect(x: 0, y: 0, width: width, height: height).fill()

  func centered(_ text: String, font: NSFont, color: NSColor, top: CGFloat) {
    let attrs: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: color]
    let size = text.size(withAttributes: attrs)
    text.draw(at: NSPoint(x: (width - size.width) / 2, y: height - top - size.height), withAttributes: attrs)
  }
  centered("Install Webbite Brick", font: .systemFont(ofSize: 26, weight: .bold), color: titleColor, top: 48)
  centered("Drag the app onto the Applications folder", font: .systemFont(ofSize: 14), color: subtitleColor, top: 88)

  // The arrow: a rounded shaft and a solid head, centred between the icons.
  let y = height - iconY
  let gap: CGFloat = 26
  let start = appX + iconSize / 2 + gap
  let end = appsX - iconSize / 2 - gap
  let headLength: CGFloat = 30
  let headWidth: CGFloat = 26
  arrowColor.set()
  let shaft = NSBezierPath()
  shaft.lineWidth = 8
  shaft.lineCapStyle = .round
  shaft.move(to: NSPoint(x: start, y: y))
  shaft.line(to: NSPoint(x: end - headLength + 2, y: y))
  shaft.stroke()
  let head = NSBezierPath()
  head.move(to: NSPoint(x: end, y: y))
  head.line(to: NSPoint(x: end - headLength, y: y + headWidth / 2))
  head.line(to: NSPoint(x: end - headLength, y: y - headWidth / 2))
  head.close()
  head.fill()

  NSGraphicsContext.restoreGraphicsState()
  return rep.representation(using: .png, properties: [:])!
}

let out = URL(fileURLWithPath: CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ".")
try draw(scale: 1).write(to: out.appendingPathComponent("background.png"))
try draw(scale: 2).write(to: out.appendingPathComponent("background@2x.png"))
