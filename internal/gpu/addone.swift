import Foundation
import Metal

struct Job: Codable {
    var values: [UInt32]
}

@main
struct AddOne {
    static func main() {
        do {
            try run()
        } catch {
            fputs(error.localizedDescription + "\n", stderr)
            exit(1)
        }
    }
}

func run() throws {
    let input = FileHandle.standardInput.readDataToEndOfFile()
    let job = try JSONDecoder().decode(Job.self, from: input)
    if job.values.isEmpty {
        fputs("values is empty\n", stderr)
        exit(2)
    }
    guard let device = MTLCreateSystemDefaultDevice() else {
        fputs("no Metal device\n", stderr)
        exit(1)
    }
    let source = """
    #include <metal_stdlib>
    using namespace metal;
    kernel void add_one(device uint *data [[buffer(0)]],
                         uint gid [[thread_position_in_grid]]) {
        data[gid] += 1;
    }
    """
    let library = try device.makeLibrary(source: source, options: nil)
    guard let function = library.makeFunction(name: "add_one") else {
        fputs("missing add_one\n", stderr)
        exit(1)
    }
    let pipeline = try device.makeComputePipelineState(function: function)
    let count = job.values.count
    let byteCount = count * MemoryLayout<UInt32>.size
    guard let buffer = device.makeBuffer(bytes: job.values, length: byteCount, options: .storageModeShared) else {
        fputs("buffer alloc failed\n", stderr)
        exit(1)
    }
    guard let queue = device.makeCommandQueue(),
          let commandBuffer = queue.makeCommandBuffer(),
          let encoder = commandBuffer.makeComputeCommandEncoder() else {
        fputs("command queue failed\n", stderr)
        exit(1)
    }
    encoder.setComputePipelineState(pipeline)
    encoder.setBuffer(buffer, offset: 0, index: 0)
    let width = max(1, min(pipeline.threadExecutionWidth, count))
    encoder.dispatchThreads(
        MTLSize(width: count, height: 1, depth: 1),
        threadsPerThreadgroup: MTLSize(width: width, height: 1, depth: 1)
    )
    encoder.endEncoding()
    commandBuffer.commit()
    commandBuffer.waitUntilCompleted()
    if let err = commandBuffer.error {
        fputs(err.localizedDescription + "\n", stderr)
        exit(1)
    }
    let ptr = buffer.contents().bindMemory(to: UInt32.self, capacity: count)
    let out = Array(UnsafeBufferPointer(start: ptr, count: count))
    let encoded = try JSONEncoder().encode(Job(values: out))
    FileHandle.standardOutput.write(encoded)
}
