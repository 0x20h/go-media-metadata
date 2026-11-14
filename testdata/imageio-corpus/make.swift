// Writes the ImageIO corpus: JPEG and HEIC files whose metadata Apple's
// ImageIO wrote, from a synthetic image. Run on macOS:
//
//	swift testdata/imageio-corpus/make.swift testdata/imageio-corpus
//
// Properties go in as ImageIO's dictionaries (Exif, GPS, IPTC, the
// orientation); XMP is then merged in with CGImageDestinationCopyImageSource,
// ImageIO's lossless metadata update, as Apple's apps write it.
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let out = URL(fileURLWithPath: CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ".")

// A 64×48 gradient with a red square: no photograph.
func baseImage() -> CGImage {
    let w = 64, h = 48
    let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0,
                        space: CGColorSpace(name: CGColorSpace.sRGB)!,
                        bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
    for y in 0..<h {
        for x in 0..<w {
            ctx.setFillColor(red: CGFloat(x) / CGFloat(w), green: CGFloat(y) / CGFloat(h), blue: 0.5, alpha: 1)
            ctx.fill(CGRect(x: x, y: y, width: 1, height: 1))
        }
    }
    ctx.setFillColor(red: 1, green: 0, blue: 0, alpha: 1)
    ctx.fill(CGRect(x: 4, y: 32, width: 12, height: 12))
    return ctx.makeImage()!
}

let exifProps: [CFString: Any] = [
    kCGImagePropertyOrientation: 6,
    kCGImagePropertyExifDictionary: [
        kCGImagePropertyExifDateTimeOriginal: "2025:05:04 13:14:15",
        kCGImagePropertyExifSubsecTimeOriginal: "25",
        kCGImagePropertyExifOffsetTimeOriginal: "+02:00",
    ] as [CFString: Any],
    kCGImagePropertyGPSDictionary: [
        kCGImagePropertyGPSLatitude: 51.5, kCGImagePropertyGPSLatitudeRef: "N",
        kCGImagePropertyGPSLongitude: 0.125, kCGImagePropertyGPSLongitudeRef: "W",
        kCGImagePropertyGPSAltitude: 35.5, kCGImagePropertyGPSAltitudeRef: 0,
    ] as [CFString: Any],
]

let iptcProps: [CFString: Any] = [
    kCGImagePropertyIPTCDictionary: [
        kCGImagePropertyIPTCObjectName: "Grün",
        kCGImagePropertyIPTCCaptionAbstract: "Am See",
        kCGImagePropertyIPTCKeywords: ["Brücke", "See"],
    ] as [CFString: Any],
]

// XMP: title and description (language alternatives), keywords, rating.
func xmp(title: String, description: String, keywords: [String], rating: Int) -> CGImageMetadata {
    let m = CGImageMetadataCreateMutable()
    let dc = "http://purl.org/dc/elements/1.1/" as CFString
    let xmpNS = "http://ns.adobe.com/xap/1.0/" as CFString
    func set(_ ns: CFString, _ prefix: String, _ name: String, _ type: CGImageMetadataType, _ value: CFTypeRef) {
        let tag = CGImageMetadataTagCreate(ns, prefix as CFString, name as CFString, type, value)!
        CGImageMetadataSetTagWithPath(m, nil, "\(prefix):\(name)" as CFString, tag)
    }
    // A language alternative is made empty, then given its x-default item
    // by path: a dictionary or array value comes out as an empty rdf:Alt.
    for (name, value) in [("title", title), ("description", description)] {
        set(dc, "dc", name, .alternateText, [] as CFArray)
        CGImageMetadataSetValueWithPath(m, nil, "dc:\(name)[x-default]" as CFString, value as CFString)
    }
    set(dc, "dc", "subject", .arrayUnordered, keywords as CFArray)
    set(xmpNS, "xmp", "Rating", .string, "\(rating)" as CFString)
    return m
}

func write(_ name: String, _ type: UTType, _ props: [CFString: Any], xmp meta: CGImageMetadata? = nil) {
    let url = out.appendingPathComponent(name)
    let data = NSMutableData()
    let dest = CGImageDestinationCreateWithData(data, type.identifier as CFString, 1, nil)!
    var p = props
    p[kCGImageDestinationLossyCompressionQuality] = 0.8
    CGImageDestinationAddImage(dest, baseImage(), p as CFDictionary)
    precondition(CGImageDestinationFinalize(dest), "writing \(name)")
    guard let meta else {
        try! (data as Data).write(to: url)
        return
    }
    // ImageIO's lossless metadata update: the image data is copied.
    let src = CGImageSourceCreateWithData(data, nil)!
    let merged = NSMutableData()
    let dest2 = CGImageDestinationCreateWithData(merged, type.identifier as CFString, 1, nil)!
    let opts: [CFString: Any] = [kCGImageDestinationMetadata: meta, kCGImageDestinationMergeMetadata: true]
    var err: Unmanaged<CFError>?
    precondition(CGImageDestinationCopyImageSource(dest2, src, opts as CFDictionary, &err), "merging XMP into \(name)")
    try! (merged as Data).write(to: url)
}

let lake = xmp(title: "Lake", description: "A lake in the mountains", keywords: ["lake", "snow"], rating: 4)
let all = exifProps.merging(iptcProps) { a, _ in a }
for (ext, type) in [("jpg", UTType.jpeg), ("heic", UTType.heic)] {
    write("exif.\(ext)", type, exifProps)
    write("xmp.\(ext)", type, [:], xmp: lake)
    write("all.\(ext)", type, all, xmp: lake)
}
write("iptc.jpg", .jpeg, iptcProps)
