# ntgpuimage

[![CI](https://github.com/NimbleTerminal/ntgpuimage/actions/workflows/ci.yml/badge.svg)](https://github.com/NimbleTerminal/ntgpuimage/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/nimbleterminal.dev/ntgpuimage.svg)](https://pkg.go.dev/nimbleterminal.dev/ntgpuimage)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE.txt)

[`ntgpuimage`](https://github.com/NimbleTerminal/ntgpuimage) is an offscreen WebGPU rendering for Golang, built on
[gogpu/wgpu](https://github.com/gogpu/wgpu). The Go package is `ntgpuimage`, imported as `nimbleterminal.dev/ntgpuimage`.

Render into a GPU texture with your own shaders, upload and sample Go
images, and read pixels back as `*image.NRGBA`. There is no window and no
event loop. The same API builds natively and for browser WebAssembly
(`GOOS=js GOARCH=wasm`).

The library manages the render target, texture resources, GPU execution, and
readback. Shaders, pipelines, buffers, and drawing use upstream WebGPU types
directly.

The current release is v0.6.0. The API may change before v1.0.
[DESIGN.md](DESIGN.md) explains the design and compares related GoGPU libraries.
[VALIDATION.md](VALIDATION.md) records what was tested and how.

## Quickstart

Install the library:

```sh
go get nimbleterminal.dev/ntgpuimage@v0.6.0
```

```go
import (
	"github.com/gogpu/gputypes"
	"github.com/gogpu/wgpu"

	"nimbleterminal.dev/ntgpuimage"
)

r, err := ntgpuimage.New(ctx, ntgpuimage.Options{Width: 640, Height: 480, Depth: true})
if err != nil { /* errors.Is(err, ntgpuimage.ErrNoAdapter), or device creation failed */ }
defer r.Close()

// Create pipelines, shaders, and buffers on the renderer's GPU thread.
err = r.Do(func(dev *wgpu.Device) error { /* dev.CreateRenderPipeline(...) */ return nil })

img, stats, err := r.Render(ctx, func(f *ntgpuimage.Frame) error {
	// f.Device.Queue().WriteBuffer(...) updates uniforms for this frame.
	pass, err := f.Encoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		ColorAttachments: []wgpu.RenderPassColorAttachment{{
			View: f.Color, LoadOp: gputypes.LoadOpClear, StoreOp: gputypes.StoreOpStore,
			ClearValue: gputypes.Color{R: 0.1, G: 0.1, B: 0.1, A: 1},
		}},
		DepthStencilAttachment: &wgpu.RenderPassDepthStencilAttachment{
			View: f.Depth, DepthLoadOp: gputypes.LoadOpClear, DepthStoreOp: gputypes.StoreOpStore,
			DepthClearValue: 1, StencilReadOnly: true, // required in the browser
		},
	})
	if err != nil { return err }
	// pass.SetPipeline(...); pass.Draw(...)
	return pass.End()
})
// img is a new *image.NRGBA that the caller owns.
```

Run the examples:

```sh
go run ./examples/clear -out frame.png      # clear a target and read it back
go run ./examples/texture -out texture.png  # upload, sample, update, read back
go run ./examples/twopass -out twopass.png  # render into a texture, then sample it
```

The interactive browser showcase uses the same two-pass Go renderer. It shows
the intermediate texture and final frame side by side, with controls for ring
frequency, tiling, and texture resolution, plus a PNG download:

```sh
task serve-site  # open http://localhost:8080
```

This requires Go, Task, Python 3, and a browser with WebGPU support. To build
without Task or Python, run `sh scripts/build-site.sh` and serve `bin/site/`
with a static HTTP server. Use localhost or HTTPS for WebGPU. All rendering,
pixel readback, and PNG encoding happen locally through Go/WASM; no server
processing is needed. The elapsed time shown includes rendering, both pixel
readbacks, PNG encoding, and browser image decoding.

The [Pages workflow](.github/workflows/pages.yml) builds the website on pull
requests and deploys pushes to `main`. Enable **Settings → Pages → Source →
GitHub Actions** in the repository before the first deployment. Assets use
relative paths so the site also works under the repository's Pages subpath.

## API

| Call | Purpose |
| --- | --- |
| `New(ctx, Options)` | Create the device and the first target. |
| `NewFromDevice(dev, Options)`, `NewFromProvider(provider, Options)` | Borrow a host device; execute on the caller's goroutine. |
| `Renderer.Do(func(*wgpu.Device) error)` | Run serialized setup or teardown. |
| `Renderer.Render(ctx, draw)` | Draw, then read the frame back in the same submission. |
| `Renderer.RenderToTexture(ctx, draw)` | Draw without reading back. |
| `Renderer.ReadPixels(ctx)` | Read back the most recent frame. |
| `Renderer.ReadPixelsInto(ctx, dst)` | Read into reusable NRGBA storage, including subimages. |
| `Renderer.ReadRegion(ctx, rect)` | Read a rectangle from the main color target. |
| `Renderer.ReadDepth(ctx, x, y)` | Read one depth value from a Depth32Float main target. |
| `Renderer.RenderTexture(enc, texture, draw)` | Record a texture draw and mip generation in a host encoder. |
| `Renderer.Resize(w, h)`, `Size()` | Replace the target. A failed resize keeps the old one. |
| `Renderer.AdapterInfo()` | Adapter name, backend, and device type. |
| `Renderer.UploadTexture(image, TextureOptions)` | Create a sampled texture from a Go image. |
| `Renderer.NewTexture(w, h, TextureOptions)` | Create a blank texture, typically a `RenderTarget`. |
| `Frame.RenderTo(texture, draw)` | Draw into a `RenderTarget` texture within the current frame. |
| `Texture.Update`, `UpdateLevels`, `ReadPixels`, `Size`, `Format`, `MipLevelCount`, `Close` | Replace pixels in place, copy them back, report the storage format, release. |
| `Texture.BindGroup()`, `BindGroupLayout()` | Handles for the caller's pipeline. |
| `Renderer.Close()` | Release everything, including textures still open. |

`Frame` gives the draw callback `Device`, `Encoder`, `Color`, `Depth`,
`Width`, `Height`, and `Seq`. `Stats` reports `Draw`, `Submit`, `Map`,
`Copy`, and `Total` durations for a frame.

## Pixels and alpha

- The main target and default textures are `RGBA8Unorm`. Colors are not
  converted between color spaces. `TextureOptions.SRGB` and
  `TextureOptions.Float16` select other texture formats; see
  [Texture formats](#texture-formats).
- Readback returns the target's bytes as `image.NRGBA`. If a shader writes
  premultiplied color, un-premultiply before treating the image as straight
  alpha. Opaque output needs no conversion.
- `UploadTexture` accepts any `image.Image`, including sub-images and padded
  strides, and stores straight alpha. Linear filtering of straight alpha
  darkens edges next to transparent texels; use an opaque image, nearest
  filtering, or a padded atlas where that matters.
- GoGPU's `gogpu` and `gg` libraries use premultiplied alpha. Convert when
  exchanging images with them.

## Sampled images

```go
tile, err := r.UploadTexture(sourceImage, ntgpuimage.TextureOptions{})
if err != nil { /* handle error */ }
defer tile.Close()

// Include tile.BindGroupLayout() in your pipeline layout. In WGSL:
// @group(0) @binding(0) var tileTexture: texture_2d<f32>;
// @group(0) @binding(1) var tileSampler: sampler;
// Inside a draw callback, bind tile.BindGroup() at that group index.

err = tile.Update(nextImage)         // same dimensions; the bind group stays valid
pixels, err := tile.ReadPixels(ctx)  // optional CPU copy of the texture
```

`TextureOptions{Filter: gputypes.FilterModeNearest}` selects nearest
filtering. The zero value selects linear. UVs clamp at the edges unless
`AddressModeU` or `AddressModeV` selects `gputypes.AddressModeRepeat` or
`gputypes.AddressModeMirrorRepeat`. Each axis is configured independently.

## Mipmaps and repeating textures

`TextureOptions.Mipmaps` generates a full mip chain on upload and regenerates it
on `Update`. Linear filtering blends between levels (trilinear filtering);
nearest filtering chooses the nearest level. The zero value keeps one level.

```go
tex, err := r.UploadTexture(pixels, ntgpuimage.TextureOptions{
    Mipmaps: true,
    Anisotropy: 16,
    SRGB: true,
    AddressModeU: gputypes.AddressModeRepeat,
    AddressModeV: gputypes.AddressModeRepeat,
})
```

`Anisotropy` improves detail when the texture footprint is stretched, such as
terrain seen at a grazing angle. Zero or 1 disables it; 2 through 16 selects the
sampler's maximum anisotropy. Values above 16 are rejected. Values above 1 require
linear filtering (the default); combining them with nearest `Filter` is an error.
The mag, min, and mip filters are all linear in this case. This option applies to
ordinary uploads, caller-supplied levels, and render targets, including Float16.
It does not require `Mipmaps`, although a full mip chain is useful for minification.
Actual quality and sampling cost depend on the backend and device.

Shaders must allow mip selection: `textureSampleLevel(..., 0.0)` and
`textureLoad(..., 0)` always use the base level. Use `textureSample` in uniform
control flow, or compute `dpdx(uv)` and `dpdy(uv)` before material-dependent
branches and pass them to `textureSampleGrad`. Take derivatives of the unwrapped
UVs and let the repeating sampler wrap them; wrapping UVs before taking
derivatives introduces seams and incorrect LODs.

For a chain shared with a CPU renderer, or alpha-test coverage adjusted by the
caller, generate or provide levels explicitly:

```go
levels, err := ntgpuimage.MipChain(pixels, ntgpuimage.MipOptions{
    SRGB: true,
    AlphaWeighted: true,
})
// Each level is a mutable *image.NRGBA. Adjust alpha for the material's
// cutout threshold here, if needed, and retain these same levels on the CPU.
tex, err := r.UploadTextureLevels(levels, ntgpuimage.TextureOptions{
    SRGB: true,
    AddressModeU: gputypes.AddressModeRepeat,
    AddressModeV: gputypes.AddressModeRepeat,
})
// Replace the whole chain later with tex.UpdateLevels(levels).
```

`UploadTextureLevels` includes level 0 and uploads the supplied bytes without
resizing, gamma conversion, alpha rescaling, or automatic generation. Each next
level halves each dimension, rounding down with a minimum of 1. Partial chains
are allowed, and sampling clamps to the last supplied level. `UpdateLevels`
requires the original count and dimensions. `ReadPixels` always returns level 0.

The CPU generator uses an area-weighted box filter, including the edges of odd
sizes and rectangular textures such as 384×256. Levels whose axes halve exactly,
including the 1×N tail of a chain, take a direct 2×2 reduction with identical
output, so power-of-two textures generate fastest. With `SRGB`, RGB is averaged
in linear light and encoded again with an exact threshold lookup; alpha stays linear. Without `SRGB`, channels stay in their numeric space.
`MipAlphaWeighted` on automatic uploads, or `MipOptions.AlphaWeighted` on the CPU
helper, weights RGB by alpha to avoid dark fringes in transparent color images.
Leave it false for data maps. Neither mode preserves alpha-test coverage;
nt3d can adjust the generated alpha before uploading explicit levels.

Mipmaps apply to uploaded and updated 8-bit textures, including blank
`NewTexture` images subsequently filled by `Update`, and to render targets of
every format. A `RenderTarget` with `Mipmaps`, or one created from a supplied
chain, regenerates every lower level on the GPU after each `Frame.RenderTo`,
inside the same frame, and after `Update`. Each level samples the level above
it once per texel with a linear clamping sampler: for even sizes that is the
2×2 box average, for odd sizes a bilinear blend of the nearest source texels.
An `SRGB` target averages in linear light and encodes on write; alpha is not
weighted into RGB. Generated levels stay within 2/255 per channel of
`MipChain` over a 64×32 chain on Metal, Chrome, and Vulkan. The `Depth`
attachment keeps one level, `ReadPixels` returns level 0, and such a texture
can be drawn into once per frame. Because gogpu v0.34.5 reports every texture
view at the base size, each level is drawn into a persistent scratch target of
its own size and copied into place; the scratch targets hold about a third of
level 0.

## Texture formats

| Option | Format | Shaders see | Upload, `Update`, `ReadPixels` |
| --- | --- | --- | --- |
| (default) | `RGBA8Unorm` | the stored values | the stored bytes |
| `SRGB: true` | `RGBA8UnormSrgb` | linear values, decoded before filtering | the stored, sRGB-encoded bytes |
| `Float16: true` | `RGBA16Float` | the stored values, including outside [0, 1] | upload and update rejected; readback clamped to 8 bits |

`SRGB` suits ordinary images that a renderer lights or blends in linear
light. Sampling decodes each texel before the sampler filters, so a
black/white edge filtered halfway gives 0.5 linear (encoded 188) rather than
encoded 0.5 (about 21% light). Remove any decode the shader did after
sampling. The bytes you upload and read back do not change.

Drawn into with `RenderTarget`, an `SRGB` texture encodes what the fragment
shader writes and blends in linear light, so shaders output linear color.
Pipelines that draw into a texture name `texture.Format()` as their color
target format. Alpha is never encoded.

`Float16` requires `RenderTarget` and suits a linear scene that is later
resolved or tone mapped into an 8-bit target. Read it in a later pass
through `BindGroup()` with `textureSample` or `textureLoad`. `Depth: true`
and `Mipmaps: true` work with every format. `SRGB` and `Float16` are exclusive.

## Rendering into a texture

A texture created with `RenderTarget: true` can be drawn into by one pass
and sampled by a later pass, in the same frame or a later one, without a
CPU readback in between. `Frame.RenderTo` hands the draw callback a `Frame`
whose `Color`, `Depth`, `Width`, and `Height` describe the texture, so the
same drawing code works against either target.

```go
scratch, err := r.NewTexture(512, 512, ntgpuimage.TextureOptions{RenderTarget: true, Depth: true})
if err != nil { /* handle error */ }
defer scratch.Close()

img, _, err := r.Render(ctx, func(f *ntgpuimage.Frame) error {
	// Pass 1: draw into the texture. t.Color and t.Depth are its views.
	if err := f.RenderTo(scratch, func(t *ntgpuimage.Frame) error {
		return drawScene(t) // BeginRenderPass on t.Encoder with t.Color
	}); err != nil {
		return err
	}
	// Pass 2: sample scratch.BindGroup() while drawing into f.Color.
	return drawPostProcess(f, scratch)
})
```

- Attach the views of the nested `Frame`, not the outer one. The nested
  `Frame` is cleared when its callback returns.
- `RenderTo` records the layout transitions the pass needs on Vulkan, and
  `ReadPixels` on a drawn texture goes through a GPU blit rather than a
  direct copy, so both pass the Vulkan validation layer; see
  [VALIDATION.md](./VALIDATION.md). Both are no-ops on Metal and in the
  browser.
- A render target has a fixed size. To change it, create a new texture and
  close the old one. `Renderer.Resize` does not affect textures.
- `Depth: true` allocates a `Depth24Plus` attachment for the texture and
  requires `RenderTarget`. The nested `Frame.Depth` is nil otherwise.
- `Mipmaps: true` regenerates the texture's mip chain on the GPU after each
  `RenderTo`; see [Mipmaps and repeating textures](#mipmaps-and-repeating-textures).
- `UploadTexture` also accepts `RenderTarget`, for drawing over an image.
- `RenderTo` cannot be nested. An error from its callback fails the frame
  even if the outer callback ignores it. Sampling a texture that has never
  been written or drawn reads undefined pixels, and `ReadPixels` on it
  returns an error.

## Threading and ownership

- A renderer created with `New` owns its instance, adapter, device, target, and textures,
  and releases them in `Close`. Calls that need the device return
  `ErrClosed` after it closes.
- With `New`, GPU work runs on one goroutine pinned to an OS thread. Calls
  from any goroutine are serialized, with one frame in flight.
- Do not call methods that queue GPU work from inside `Do` or a draw
  callback. They would wait for the thread they already occupy. Texture
  handle accessors such as `BindGroup` and `BindGroupLayout` are safe to
  use in callbacks.
- Objects you create with the device are yours. Release them inside `Do`
  before `Close`.
- Do not keep the device, encoder, or views from a callback for use on
  another goroutine.
- `NewFromDevice` and `NewFromProvider` borrow the device. Their methods run
  on the caller's goroutine, serialized with a cancellable lock. The host must
  enforce thread affinity and coordinate other users of the device and queue.
  Call `Close` on the host thread before releasing the host device. It releases
  only renderer-created resources. Adapter-selection options are ignored.

## Using a host device

```go
// Run on the host's GPU thread. No main target is allocated yet.
r, err := ntgpuimage.NewFromDevice(dev, ntgpuimage.Options{})
if err != nil { return err }
// Keep r alive until all host work using its textures has finished.

texture, err := r.NewTexture(512, 512, ntgpuimage.TextureOptions{
    RenderTarget: true, Mipmaps: true, SRGB: true,
})
if err != nil { return err }
err = r.RenderTexture(enc, texture, func(f *ntgpuimage.Frame) error {
    return drawProcedural(f) // record and end passes using f.Color
})
if err != nil { return err } // host discards enc on failure
// The host may now sample texture.BindGroup() in a later pass of enc.
// The host finishes and submits enc.
```

Submit host encoders in recording order, before using affected textures in
another encoder, an update, or readback. Later passes in the same encoder may
sample them. Do not release their resources until the GPU finishes. Render a
mipmapped texture at most once per encoder. If an encoder with successful
`RenderTexture` calls is abandoned or fails submission, recreate its affected
textures: their tracked layouts assume those commands execute. A callback
must not call renderer methods; it already holds the serialization lock.

Both constructors accept `Options{Width: 0, Height: 0}`. Use `Resize(w, h)`
before rendering or reading the main target; `Render` cannot infer dimensions
from its callback. `NewFromDevice` reports zero adapter metadata;
`NewFromProvider` reports it when the provider exposes a wgpu adapter.

For captures, `RenderToTexture` followed by `ReadPixelsInto(ctx, dst)` reuses
`dst.Pix`. For picking, `ReadRegion(ctx, image.Rect(x, y, x+1, y+1))` copies
only one color pixel. Enable `Options.DepthFormat: gputypes.TextureFormatDepth32Float`
and store depth in the render pass to use `ReadDepth(ctx, x, y)`. It returns
the raw stored depth; WebGPU requires a full depth-subresource copy even
for a single pixel. All these readbacks share cancellable mapping and `Stats`.

## Cancellation

`Render`, `RenderToTexture`, and all readback methods take a context.

- A call waiting for the GPU thread returns `ctx.Err()` when the context
  is cancelled or expires, without drawing.
- Cancellation during the draw callback or while the GPU finishes the
  frame also returns `ctx.Err()`. A running draw callback is not
  interrupted.
- After cancellation the GPU may still complete the abandoned frame.
  Later calls are unaffected.
- On Metal, a cancellable context added about 0.1 ms per readback in
  testing, because the wait must poll. `context.Background()` uses a
  single blocking wait.
- With a cancellable context, native `RenderToTexture` returns once the
  frame is submitted instead of waiting for the GPU.
- `Do`, `Resize`, `UploadTexture`, `Update`, and `Close` take no context and
  wait their turn.

## Browser

- Requires a WebGPU-capable browser in a secure context (HTTPS or
  localhost).
- Call the library from a Go goroutine, not from inside a JavaScript
  callback. Readback awaits a promise, which would deadlock the event loop.
- While a readback is pending, the library requests animation frames so
  that the browser settles the map promptly.
- Set `StencilReadOnly: true` when attaching `Frame.Depth`. The browser
  backend otherwise emits stencil operations, which WebGPU rejects for a
  depth-only format.
- `AdapterInfo` cannot identify a software adapter in the browser. Upstream
  reports every browser adapter as `"WebGPU Adapter"`.

## Testing

With [Task](https://taskfile.dev) installed, `task list` shows every task.

| Task | Runs | Needs |
| --- | --- | --- |
| `task` | Race tests and builds of examples, the browser runner, and WASM | Nothing |
| `task ci` | Everything CI runs: race tests, vet, WASM build, Node tests | Node |
| `task fuzz-smoke` | Every fuzz target for `FUZZTIME` (default 10s) each | Nothing |
| `task test-gpu` | The full suite on a native adapter | A GPU |
| `task bench` | Clear and readback benchmarks, three runs per case | A GPU |
| `task test-browser` | The full suite in headless Chrome | Chrome with WebGPU |
| `task validate` | `ci`, `test-gpu`, and `test-browser` | All of the above |
| `task test-vulkan` | The full suite on Vulkan under the validation layer | Linux with Vulkan |
| `task test-vulkan-remote HOST=name` | Copies the tree to `name` over ssh and runs `test-vulkan` there | ssh access |
| `task run-examples` | All three examples, writing PNG files to `bin/` | A GPU |
| `task build-site` | Build the interactive browser demo into `bin/site/` | Nothing |
| `task serve-site` | Build and serve the browser demo on localhost:8080 | Python 3; browser WebGPU to render |

The same commands without Task:

```sh
go test -race ./...                   # helpers and executor, no GPU
GPUIMAGE_GPU_TEST=1 go test ./...     # real native adapter; never with -race

# Helper tests under Node, which has no WebGPU:
GOOS=js GOARCH=wasm go test -exec="$(go env GOROOT)/lib/wasm/go_js_wasm_exec" .

# The full suite in headless Chrome with WebGPU:
go build -o /tmp/browsertest ./internal/browsertest
GPUIMAGE_GPU_TEST=1 GOOS=js GOARCH=wasm go test -exec=/tmp/browsertest -v .
```

GPU tests log the adapter they ran on. A run on a CPU or fallback adapter
does not test hardware GPU behavior.

## Benchmarks

`BenchmarkRenderReadback` measures one complete frame: clear an RGBA8
color target, submit it, wait for the GPU, and copy the pixels into a new
`image.NRGBA`. It includes allocation and garbage collection during the
loop. Device creation, ten warmup frames, context creation, and cleanup
are outside the timer. It does not draw geometry, upload textures, or use
a depth attachment.

Each size runs with `context.Background()` and with one reused cancellable
context that is never cancelled. This measures the cost of checking for
cancellation, not the cost of cancelling a frame.

```sh
task bench                            # choose the default native backend
task bench BACKEND=metal               # macOS
task bench BACKEND=vulkan              # Linux
task bench BENCHTIME=3s COUNT=5         # longer runs

# Without Task (set GPUIMAGE_BENCH_BACKEND=metal or vulkan to choose):
GPUIMAGE_GPU_TEST=1 go test -run='^$' -bench=BenchmarkRenderReadback \
  -benchmem -benchtime=1s -count=3 .
```

Run without the race detector or Vulkan validation layers. The benchmark
logs the adapter and rejects software adapters. `MB/s` is the output image
size divided by frame time; it is not a measurement of GPU bus bandwidth.
Allocation counts cover Go memory, not GPU memory.

Results from 2026-09-27, using wgpu v0.34.5 and naga v0.19.0. Each value
is the median of three runs with `-benchtime=1s`. Lower is better.

| Frame size | Context | Apple M3 Max / Metal | NVIDIA GB10 / Vulkan |
| --- | --- | --- | --- |
| 640x400 | Background | 0.587 ms | 0.480 ms |
| 640x400 | Cancellable | 0.645 ms | 0.684 ms |
| 1280x720 | Background | 0.657 ms | 0.768 ms |
| 1280x720 | Cancellable | 0.758 ms | 0.938 ms |
| 1920x1080 | Background | 0.820 ms | 1.077 ms |
| 1920x1080 | Cancellable | 0.954 ms | 1.199 ms |

The Metal run used macOS 27.0 arm64 and Go 1.27.1. The Vulkan run used
Ubuntu 24.04.5 arm64, Go 1.26.8, and NVIDIA driver 580.178.04. Vulkan used
`VK_DRIVER_FILES=/usr/share/vulkan/icd.d/nvidia_icd.json` to select the
NVIDIA driver. These numbers compare complete systems, including CPU,
driver, and Go runtime costs.

Raw output, including throughput and allocations:
[Metal](benchmarks/2026-09-27-metal.txt) and
[Vulkan](benchmarks/2026-09-27-vulkan.txt).

## Known limitations

- naga v0.19.0 generates invalid Metal source for arithmetic on a component
  of an indexed `let` or `const` array, such as `positions[i].x * 0.5`.
  List the values explicitly, as the examples do. `TestDerivedUVShader`
  reports when upstream fixes this.
- The main color target cannot be sampled by a later pass. It has no
  `TextureBinding` usage. Draw into a `RenderTarget` texture with
  `Frame.RenderTo` instead.
- Render-target textures have no resize; create a new one.
- No MSAA or partial texture updates. The main target is always `RGBA8Unorm`
  and has no mip chain; draw into a `RenderTarget` texture with `Mipmaps` for one.
- A mipmapped `RenderTarget` accepts one `Frame.RenderTo` per frame.
- On desktop OpenGL, gogpu does not enable `GL_FRAMEBUFFER_SRGB`, so drawing
  into an `SRGB` texture there probably stores linear values unencoded.
  Sampling decodes as elsewhere. This follows from reading gogpu's source
  and has not been tested.
- Metal, Vulkan on one NVIDIA adapter, and Chrome were run. DX12, GLES, and
  other browsers compile but were not tested.
- `ReadPixels` fails until a frame has been drawn to the current target,
  and `Texture.ReadPixels` fails until the texture has been written or
  drawn into.


## Open Collaboration

We welcome contributions and feedback.  Please adhere to our [Code of Conduct](./CODE_OF_CONDUCT.md) when engaging our community.

 * [GitHub Issues](https://github.com/NimbleTerminal/ntgpuimage/issues)
 * [GitHub Pull Requests](https://github.com/NimbleTerminal/ntgpuimage/pulls)

## Acknowledgements

Thanks to the [GoGPU organization](https://gogpu.io) for their dedication to making GPU processing available to Golang developers, especially in the form of the [`wgpu` WebGPU library](https://github.com/gogpu/wgpu).

## License

This project is released under the [MIT License](https://en.wikipedia.org/wiki/MIT_License), see [LICENSE.txt](./LICENSE.txt).

Copyright (c) 2026 [Neomantra Corp](https://www.neomantra.com).   

----
Made with :heart: and :fire: by the team behind [Nimble.Markets](https://nimble.markets).
