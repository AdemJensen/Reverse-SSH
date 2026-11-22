import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let output = URL(fileURLWithPath: CommandLine.arguments[1])
let sizes: [(String, Int)] = [
    ("icp4", 16), ("icp5", 32), ("icp6", 64), ("ic07", 128),
    ("ic08", 256), ("ic09", 512), ("ic10", 1024),
]

func color(_ red: CGFloat, _ green: CGFloat, _ blue: CGFloat) -> CGColor {
    CGColor(red: red / 255, green: green / 255, blue: blue / 255, alpha: 1)
}

func appendLength(_ length: Int, to buffer: inout Data) {
    var value = UInt32(length).bigEndian
    withUnsafeBytes(of: &value) { buffer.append(contentsOf: $0) }
}

var entries = Data()
for (tag, size) in sizes {
    let context = CGContext(data: nil, width: size, height: size, bitsPerComponent: 8, bytesPerRow: 0,
                            space: CGColorSpace(name: CGColorSpace.sRGB)!,
                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
    context.scaleBy(x: CGFloat(size) / 1024, y: CGFloat(size) / 1024)
    let tile = CGRect(x: 105, y: 105, width: 814, height: 814)
    context.saveGState()
    context.setShadow(offset: CGSize(width: 0, height: -16), blur: 32, color: CGColor(gray: 0, alpha: 0.22))
    context.setFillColor(color(186, 231, 212))
    context.addPath(CGPath(roundedRect: tile, cornerWidth: 193, cornerHeight: 193, transform: nil))
    context.fillPath()
    context.restoreGState()

    context.setStrokeColor(color(19, 63, 53))
    context.setLineWidth(61)
    context.setLineCap(.round)
    context.setLineJoin(.round)
    context.move(to: CGPoint(x: 298, y: 611))
    context.addLine(to: CGPoint(x: 707, y: 611))
    context.move(to: CGPoint(x: 608, y: 710))
    context.addLine(to: CGPoint(x: 707, y: 611))
    context.addLine(to: CGPoint(x: 608, y: 512))
    context.move(to: CGPoint(x: 726, y: 413))
    context.addLine(to: CGPoint(x: 317, y: 413))
    context.move(to: CGPoint(x: 416, y: 512))
    context.addLine(to: CGPoint(x: 317, y: 413))
    context.addLine(to: CGPoint(x: 416, y: 314))
    context.strokePath()

    let png = NSMutableData()
    let destination = CGImageDestinationCreateWithData(png, UTType.png.identifier as CFString, 1, nil)!
    CGImageDestinationAddImage(destination, context.makeImage()!, nil)
    precondition(CGImageDestinationFinalize(destination))
    entries.append(contentsOf: tag.utf8)
    appendLength(png.length + 8, to: &entries)
    entries.append(png as Data)
}

var icon = Data("icns".utf8)
appendLength(entries.count + 8, to: &icon)
icon.append(entries)
try icon.write(to: output, options: .atomic)
