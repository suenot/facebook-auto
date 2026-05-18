# w-popularity-parser-facebook

`facebook` parser for [w_popularity](https://github.com/suenot/w-popularity).

**Status:** stub. `FetchChannel` and `FetchRecentPosts` return `shared.ErrNotImplemented`.

## Strategy

- **Primary:** Graph API with page access token
- **Fallback:** camoufox

## Usage

```go
import parser "github.com/suenot/w-popularity-parser-facebook"

p := parser.New(parser.Config{Credential: os.Getenv("CRED")})
snap, err := p.FetchChannel(ctx, handle)
```

## License

MIT
