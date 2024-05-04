<p align="center">
    <a href="https://github.com/xamarnath/gogram">
        <img src="https://i.imgur.com/RE1M0sM.png" alt="Gogram" width="256">
    </a>
    <br>
    <b>Telegram MTProto API Framework for Golang</b>
    <br>
    <a href="/">
        Homepage
    </a>
    •
    <a href="/examples/">
        Docs
    </a>
    •
    <a href="https://github.com/xamarnath/gogram/releases">
        Releases
    </a>
    •
    <a href="https://t.me/rosexchat">
        Support
    </a>
</p>

## GoGram

> Light Weight, Fast, Elegant Telegram [MTProto API](https://core.telegram.org/api) framework in [Golang](https://golang.org/) for building Telegram clients and bots.

## Status

[![GoDoc](https://godoc.org/github.com/xamarnath/gogram?status.svg)](https://godoc.org/github.com/xamarnath/gogram)
[![Go Report Card](https://goreportcard.com/badge/github.com/xamarnath/gogram)](https://goreportcard.com/report/github.com/xamarnath/gogram)
[![License](https://img.shields.io/github/license/xamarnath/gogram.svg)](https://img.shields.io/github/license/xamarnath/gogram.svg)
[![GitHub stars](https://img.shields.io/github/stars/xamarnath/gogram.svg?style=social&label=Stars)](
    https://img.shields.io/github/license/xamarnath/gogram.svg?style=social&label=Stars)
[![GitHub forks](https://img.shields.io/github/forks/xamarnath/gogram.svg?style=social&label=Fork)](
    https://img.shields.io/github/license/xamarnath/gogram.svg?style=social&label=Fork)
[![GitHub issues](https://img.shields.io/github/issues/xamarnath/gogram.svg)](
    https://img.shields.io/github/license/xamarnath/gogram.svg
)


``` golang
package main

import "github.com/xamarnath/gogram/telegram"

func main() {
    client, err := telegram.NewClient(&telegram.ClientConfig{
        AppID: 6, AppHash: "<app-hash>",
        // StringSession: "<string-session>",
    })

    client.ConnectBot("<bot-token>") // or client.Login("<phone-number>") for user account
    // client.AuthPrompt() // for console-based interactive auth

    client.AddMessageHandler(telegram.OnNewMessage, func(message *telegram.NewMessage) error {
        if m.IsPrivate() {
            m.Reply("Hello from Gogram!")
        }
    })

    client.Idle() // block main goroutine until client is closed
}
```

**Gogram** is a modern, elegant and concurrent [MTProto API](https://core.telegram.org/api)
framework. It enables you to easily interact with the main Telegram API through a user account (custom client) or a bot
identity (bot API alternative) using Go.

## Support

If you'd like to support Gogram, you can consider:

- [Become a GitHub sponsor](https://github.com/sponsors/xamarnath).

## Key Features

- **Ready**: Install Gogram with go get and you are ready to go!
- **Easy**: Makes the Telegram API simple and intuitive, while still allowing advanced usages.
- **Elegant**: Low-level details are abstracted and re-presented in a more convenient way.
- **Fast**: Backed by a powerful and concurrent library, Gogram can handle even the heaviest workloads.
- **Zero Dependencies**: No need to install anything else than Gogram itself.
- **Powerful**: Full access to Telegram's API to execute any official client action and more.
- **Feature-Rich**: Built-in support for file uploading, formatting, custom keyboards, message editing, moderation tools and more.
- **Up-to-date**: Gogram is always in sync with the latest Telegram API changes and additions (`tl-parser` is used to generate the API layer).

#### Current Layer - **179** (Updated on 2024-04-27)

## Installing

``` bash
go get -u github.com/xamarnath/gogram/telegram
```

## Doing Stuff

#### Sending a Message

``` golang
client.SendMessage("username", "Hello from Gogram!")

client.SendDice("username", "🎲")

client.AddMessageHandler("/start", func(m *telegram.Message) error {
    m.Reply("Hello from Gogram!") // m.Respond("<text>")
    return nil
})
```

#### Sending Media

``` golang
client.SendMedia("username", "<file-name>", &telegram.MediaOptions{ // filename/inputmedia,...
    Caption: "Hello from Gogram!",
    TTL: int32((math.Pow(2, 31) - 1)), //  TTL For OneTimeMedia
})

client.SendAlbum("username", []string{"<file-name>", "<file-name>"}, &telegram.MediaOptions{ // Array of filenames/inputmedia,...
    Caption: "Hello from Gogram!",
})
```

#### Inline Queries

``` golang
client.AddInlineHandler("<pattern>", func(iq *telegram.InlineQuery) error {
	builder := iq.Builder()
	builder.Article("<title>", "<description>", "<text>", &telegram.ArticleOptions{
			LinkPreview: true,
	})

	return nil
})
```

## Features TODO

- [x] Basic MTProto implementation (LAYER 179)
- [x] Updates handling system + Cache
- [x] HTML, Markdown Parsing, Friendly Methods
- [x] Support for Flag2.0, Layer 147
- [x] WebRTC Calls Support
- [ ] Documentation for all methods
- [x] Stabilize File Uploading
- [ ] Stabilize File Downloading
- [ ] Secret Chats Support

## Known Issues

- [x] File Uploading/Downloading is not stable
- [x] MessageMediaPoll, UserFull Decode Fails
- [x] invokeWithLayer channel missing while bad Salt
- [x] tcp.io.Reader.Read unstable
- [x] Perfect HTML Parser 
- [x] Session File some issues
- [ ] Unidentified RPCError decoding fails


## Contributing

Gogram is an open-source project and your contribution is very much appreciated. If you'd like to contribute, simply fork the repository, commit your changes and send a pull request. If you have any questions, feel free to ask.

## Resources

- Documentation: (Coming Soon)
- Support: [@rosexchat](https://t.me/rosexchat)

## License

This library is provided under the terms of the [GPL-3.0 License](LICENSE).
