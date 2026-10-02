import Foundation

/// Arbitrary JSON, used for tool inputs (`Approval.input`, `Call.input`) whose
/// shape depends on the tool.
///
/// Objects are stored as dictionaries, so the original key order is lost;
/// `prettyPrinted()` sorts keys to stay deterministic.
public enum JSONValue: Sendable, Equatable, Hashable {
    case null
    case bool(Bool)
    case int(Int64)
    case double(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    /// The value at `key` when this is an object.
    public subscript(key: String) -> JSONValue? {
        if case .object(let o) = self { return o[key] }
        return nil
    }

    public var stringValue: String? {
        if case .string(let s) = self { return s }
        return nil
    }

    public var isEmptyObject: Bool {
        switch self {
        case .null: true
        case .object(let o): o.isEmpty
        default: false
        }
    }
}

extension JSONValue: Codable {
    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() {
            self = .null
        } else if let b = try? c.decode(Bool.self) {
            self = .bool(b)
        } else if let i = try? c.decode(Int64.self) {
            self = .int(i)
        } else if let d = try? c.decode(Double.self) {
            self = .double(d)
        } else if let s = try? c.decode(String.self) {
            self = .string(s)
        } else if let a = try? c.decode([JSONValue].self) {
            self = .array(a)
        } else if let o = try? c.decode([String: JSONValue].self) {
            self = .object(o)
        } else {
            throw DecodingError.dataCorruptedError(in: c, debugDescription: "unsupported JSON value")
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .int(let i): try c.encode(i)
        case .double(let d): try c.encode(d)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }
}

extension JSONValue {
    /// Multi-line JSON with two-space indentation and sorted keys. Non-ASCII
    /// text (Korean) is kept as is rather than escaped.
    public func prettyPrinted() -> String {
        var out = ""
        write(to: &out, indent: 0, pretty: true)
        return out
    }

    /// Single-line JSON with sorted keys.
    public func compact() -> String {
        var out = ""
        write(to: &out, indent: 0, pretty: false)
        return out
    }

    private func write(to out: inout String, indent: Int, pretty: Bool) {
        switch self {
        case .null:
            out += "null"
        case .bool(let b):
            out += b ? "true" : "false"
        case .int(let i):
            out += String(i)
        case .double(let d):
            out += JSONValue.format(d)
        case .string(let s):
            JSONValue.writeString(s, to: &out)
        case .array(let a):
            if a.isEmpty { out += "[]"; return }
            out += "["
            for (n, v) in a.enumerated() {
                if n > 0 { out += "," }
                if pretty { out += "\n" + String(repeating: " ", count: indent + 2) }
                v.write(to: &out, indent: indent + 2, pretty: pretty)
            }
            if pretty { out += "\n" + String(repeating: " ", count: indent) }
            out += "]"
        case .object(let o):
            if o.isEmpty { out += "{}"; return }
            out += "{"
            for (n, key) in o.keys.sorted().enumerated() {
                if n > 0 { out += "," }
                if pretty { out += "\n" + String(repeating: " ", count: indent + 2) }
                JSONValue.writeString(key, to: &out)
                out += pretty ? ": " : ":"
                o[key]!.write(to: &out, indent: indent + 2, pretty: pretty)
            }
            if pretty { out += "\n" + String(repeating: " ", count: indent) }
            out += "}"
        }
    }

    private static func format(_ d: Double) -> String {
        guard d.isFinite else { return "null" }
        if d == d.rounded(), abs(d) < 1e15 { return String(Int64(d)) }
        return String(d)
    }

    private static func writeString(_ s: String, to out: inout String) {
        out += "\""
        for scalar in s.unicodeScalars {
            switch scalar {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "\t": out += "\\t"
            default:
                if scalar.value < 0x20 {
                    out += String(format: "\\u%04x", scalar.value)
                } else {
                    out.unicodeScalars.append(scalar)
                }
            }
        }
        out += "\""
    }
}
