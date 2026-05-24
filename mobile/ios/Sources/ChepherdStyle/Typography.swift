// Typography — §3 of the design system. Monospace default everywhere.

#if canImport(SwiftUI)
import SwiftUI

public enum ChepherdFont {
    // §3.2 Type scale.
    public static let xs:  CGFloat = 11
    public static let sm:  CGFloat = 13
    public static let base: CGFloat = 16
    public static let lg:  CGFloat = 20
    public static let xl:  CGFloat = 24
    public static let xxl: CGFloat = 32
    public static let xxxl: CGFloat = 48

    /// Monospace font matching SF Mono on iOS / macOS.
    public static func mono(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .monospaced)
    }
}
#endif
