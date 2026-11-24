import AppKit

let output = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)

func render(size: Int) -> Data {
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size, bitsPerSample: 8,
        samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    let scale = NSAffineTransform()
    scale.scale(by: CGFloat(size) / 1024.0)
    scale.concat()

    let body = NSBezierPath(roundedRect: CGRect(x: 79, y: 79, width: 866, height: 866), xRadius: 199, yRadius: 199)
    let top = NSColor(srgbRed: 0.10, green: 0.29, blue: 0.29, alpha: 1)
    let bottom = NSColor(srgbRed: 0.04, green: 0.12, blue: 0.14, alpha: 1)
    NSGradient(starting: bottom, ending: top)!.draw(in: body, angle: 90)
    NSColor.white.withAlphaComponent(0.13).setStroke()
    body.lineWidth = 7
    body.stroke()

    let grid = NSBezierPath()
    for y in [340, 512, 684] {
        grid.move(to: CGPoint(x: 258, y: y))
        grid.line(to: CGPoint(x: 767, y: y))
    }
    grid.lineWidth = 7
    grid.stroke()

    let line = NSBezierPath()
    line.move(to: CGPoint(x: 277, y: 378))
    line.line(to: CGPoint(x: 387, y: 475))
    line.line(to: CGPoint(x: 482, y: 432))
    line.line(to: CGPoint(x: 610, y: 596))
    line.line(to: CGPoint(x: 746, y: 664))
    line.lineJoinStyle = .round
    line.lineCapStyle = .round
    line.lineWidth = 46
    NSColor(srgbRed: 0.71, green: 0.94, blue: 0.78, alpha: 1).setStroke()
    line.stroke()
    NSColor(srgbRed: 0.91, green: 0.98, blue: 0.77, alpha: 1).setFill()
    NSBezierPath(ovalIn: CGRect(x: 719, y: 637, width: 54, height: 54)).fill()
    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for size in [16, 32, 128, 256, 512] {
    try render(size: size).write(to: output.appendingPathComponent("icon_\(size)x\(size).png"))
    try render(size: size * 2).write(to: output.appendingPathComponent("icon_\(size)x\(size)@2x.png"))
}
