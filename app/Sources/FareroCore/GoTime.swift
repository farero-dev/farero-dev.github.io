import Foundation

/// Parses and formats the RFC 3339 timestamps Go's `time.Time` produces:
/// `2026-10-02T09:41:37.397123+09:00`, `2026-10-02T00:41:37Z`. Fractional
/// seconds have 0 to 9 digits. Go's zero time (`0001-01-01T00:00:00Z`, sent
/// for unset fields without `omitzero`) maps to nil.
public enum GoTime {
    public static func parse(_ s: String) -> Date? {
        let b = Array(s.utf8)
        // YYYY-MM-DDTHH:MM:SS is 19 bytes, plus at least "Z".
        guard b.count >= 20 else { return nil }
        func num(_ from: Int, _ len: Int) -> Int? {
            var v = 0
            for i in from..<(from + len) {
                let c = b[i]
                guard c >= 48, c <= 57 else { return nil }
                v = v * 10 + Int(c - 48)
            }
            return v
        }
        guard let year = num(0, 4), b[4] == 45, let month = num(5, 2), b[7] == 45,
              let day = num(8, 2), b[10] == 84 || b[10] == 116 || b[10] == 32,
              let hour = num(11, 2), b[13] == 58, let minute = num(14, 2), b[16] == 58,
              let second = num(17, 2)
        else { return nil }
        guard (1...12).contains(month), (1...31).contains(day), hour < 24, minute < 60, second < 61 else { return nil }

        var i = 19
        var fraction = 0.0
        if i < b.count, b[i] == 46 { // "."
            i += 1
            var scale = 0.1
            let start = i
            while i < b.count, b[i] >= 48, b[i] <= 57 {
                fraction += Double(b[i] - 48) * scale
                scale /= 10
                i += 1
            }
            if i == start { return nil }
        }
        guard i < b.count else { return nil }
        var offset = 0
        switch b[i] {
        case 90, 122: // Z z
            i += 1
        case 43, 45: // + -
            guard b.count >= i + 6, let oh = num(i + 1, 2), b[i + 3] == 58, let om = num(i + 4, 2) else { return nil }
            offset = (oh * 3600 + om * 60) * (b[i] == 45 ? -1 : 1)
            i += 6
        default:
            return nil
        }
        guard i == b.count else { return nil }
        if year == 1 && month == 1 && day == 1 && hour == 0 && minute == 0 && second == 0 && fraction == 0 {
            return nil // Go zero time
        }
        let days = daysFromCivil(year: year, month: month, day: day)
        let unix = Double(days * 86400 + hour * 3600 + minute * 60 + second - offset) + fraction
        return Date(timeIntervalSince1970: unix)
    }

    /// RFC 3339 in UTC with millisecond precision, which Go parses.
    public static func format(_ date: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        f.timeZone = TimeZone(identifier: "UTC")
        return f.string(from: date)
    }

    /// Days since 1970-01-01 in the proleptic Gregorian calendar
    /// (Howard Hinnant's algorithm).
    static func daysFromCivil(year: Int, month: Int, day: Int) -> Int {
        let y = month <= 2 ? year - 1 : year
        let era = (y >= 0 ? y : y - 399) / 400
        let yoe = y - era * 400
        let mp = (month + 9) % 12
        let doy = (153 * mp + 2) / 5 + day - 1
        let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy
        return era * 146_097 + doe - 719_468
    }
}
