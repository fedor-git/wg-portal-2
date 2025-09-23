# wg-portal-2

[![Build Status](https://github.com/fedor-git/wg-portal-2/actions/workflows/docker-publish.yml/badge.svg?event=push)](https://github.com/fedor-git/wg-portal-2/actions/workflows/docker-publish.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](https://opensource.org/licenses/MIT)
![GitHub last commit](https://img.shields.io/github/last-commit/fedor-git/wg-portal-2/master)
[![Go Report Card](https://goreportcard.com/badge/github.com/fedor-git/wg-portal-2)](https://goreportcard.com/report/github.com/fedor-git/wg-portal-2)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/fedor-git/wg-portal-2)
![GitHub code size in bytes](https://img.shields.io/github/languages/code-size/fedor-git/wg-portal-2)
[![Forked from original-repo](https://img.shields.io/badge/Forked%20from-h44z%2Fwg--portal-blue?logo=github)](https://github.com/h44z/wg-portal)

* Added cluster mode support. It is now possible to run multiple instances with a shared database. Synchronization is handled by the fanout module, which can be configured in the `core` fanout section

```yaml
core:
 ...
 manage_dns: false
 ignore_main_default_route: true
 delete_expired_peers: true
 default_user_ttl: 2 # days

  fanout:
    enabled: true
    self_url: "{{ external_url }}"
    peers:
      - ...
      - ...
    auth_header: "Authorization"
    auth_value: "Basic ..."
    timeout: 2s
    debounce: 300ms
    origin: ""
    kick_on_start: true
    topics: ["peers.updated", "peer.save", "peer.delete", "interface.save", "interface.updated"]
```
Tested with MySQL.

* Added the `manage_dns` option. When set to true, this option disables the management of resolv.conf on the host and within the WireGuard container.

* Added the `ignore_main_default_route` option. This option disables the management of the main route table.

* Added the `delete_expired_peers` option. This enables the automatic deletion of peers once their status changes to expired.

* Added the `default_user_ttl` option. This sets a default Time-To-Live (TTL) in days for peers that are provisioned via the v1 API.



## Introduction
<!-- Text from this line # is included in docs/documentation/overview.md -->
**WireGuard Portal** is a simple, web-based configuration portal for [WireGuard](https://wireguard.com) server management.
The portal uses the WireGuard [wgctrl](https://github.com/WireGuard/wgctrl-go) library to manage existing VPN
interfaces. This allows for the seamless activation or deactivation of new users without disturbing existing VPN
connections.

The configuration portal supports using a database (SQLite, MySQL, MsSQL, or Postgres), OAuth or LDAP
(Active Directory or OpenLDAP) as a user source for authentication and profile data.

## Features

* Self-hosted - the whole application is a single binary
* Responsive multi-language web UI written in Vue.js
* Automatically selects IP from the network pool assigned to the client
* QR-Code for convenient mobile client configuration
* Sends email to the client with QR-code and client config
* Enable / Disable clients seamlessly
* Generation of wg-quick configuration file (`wgX.conf`) if required
* User authentication (database, OAuth, or LDAP), Passkey support
* IPv6 ready
* Docker ready
* Can be used with existing WireGuard setups
* Support for multiple WireGuard interfaces
* Supports multiple WireGuard backends (wgctrl or MikroTik [BETA])
* Peer Expiry Feature
* Handles route and DNS settings like wg-quick does
* Exposes Prometheus metrics for monitoring and alerting
* REST API for management and client deployment
* Webhook for custom actions on peer, interface, or user updates

<!-- Text to this line # is included in docs/documentation/overview.md -->
![Screenshot](docs/assets/images/screenshot.png)

## Documentation

For the complete documentation visit [wgportal.org](https://wgportal.org).

## What is out of scope

* Automatic generation or application of any `iptables` or `nftables` rules.
* Support for operating systems other than linux.
* Automatic import of private keys of an existing WireGuard setup.

## Application stack

* [wgctrl-go](https://github.com/WireGuard/wgctrl-go) and [netlink](https://github.com/vishvananda/netlink) for interface handling
* [Bootstrap](https://getbootstrap.com/), for the HTML templates
* [Vue.js](https://vuejs.org/), for the frontend

## License

* MIT License. [MIT](LICENSE.txt) or <https://opensource.org/licenses/MIT>


> [!IMPORTANT]
> Since the project was accepted by the Docker-Sponsored Open Source Program, the Docker image location has moved to [wgportal/wg-portal](https://hub.docker.com/r/wgportal/wg-portal).
> Please update the Docker image from **fedor-git/wg-portal-2** to **wgportal/wg-portal**.
