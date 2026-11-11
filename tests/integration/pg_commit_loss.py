"""Loopback-only PostgreSQL test proxy: drop a confirmed COMMIT response."""
import socket
import struct
import threading


def receive(connection, size):
    result = b""
    while len(result) < size:
        part = connection.recv(size - len(result))
        if not part:
            raise EOFError()
        result += part
    return result


def frame(connection):
    tag = receive(connection, 1)
    length = receive(connection, 4)
    size = struct.unpack("!I", length)[0]
    if size < 4 or size > 16 * 1024 * 1024:
        raise ValueError("invalid PostgreSQL test frame")
    body = receive(connection, size - 4)
    return tag, body, tag + length + body


class CommitLossProxy:
    def __init__(self, upstream_port):
        self.upstream_port = upstream_port
        self.listener = socket.socket()
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen(1)
        self.listener.settimeout(20)
        self.port = self.listener.getsockname()[1]
        self.committing = threading.Event()
        self.dropped = threading.Event()
        self.connections = []
        self.errors = []
        self.threads = []

    def __enter__(self):
        worker = threading.Thread(target=self.serve, daemon=True)
        self.threads.append(worker)
        worker.start()
        return self

    def close_connections(self):
        for connection in self.connections:
            try:
                connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            connection.close()

    def __exit__(self, *_):
        self.listener.close()
        self.close_connections()
        for worker in self.threads:
            worker.join(timeout=2)

    def serve(self):
        try:
            client, _ = self.listener.accept()
            server = socket.create_connection(("127.0.0.1", self.upstream_port), timeout=20)
            client.settimeout(20)
            self.connections.extend([client, server])
            # Tests deliberately use sslmode=disable on this disposable loopback DB.
            size_bytes = receive(client, 4)
            size = struct.unpack("!I", size_bytes)[0]
            if size < 8 or size > 65536:
                raise ValueError("invalid startup frame")
            server.sendall(size_bytes + receive(client, size - 4))
            backend = threading.Thread(target=self.backend, args=(server, client), daemon=True)
            self.threads.append(backend)
            backend.start()
            while True:
                tag, body, packet = frame(client)
                if tag == b"Q" and body.rstrip(b"\0").strip().upper() == b"COMMIT":
                    self.committing.set()
                server.sendall(packet)
        except (EOFError, OSError):
            if not self.dropped.is_set():
                self.errors.append("connection ended before commit response loss")
        except Exception:
            self.errors.append("proxy framing failed")
        finally:
            self.close_connections()

    def backend(self, server, client):
        commit_confirmed = False
        try:
            while True:
                tag, body, packet = frame(server)
                if self.committing.is_set():
                    if tag == b"C" and body == b"COMMIT\0":
                        commit_confirmed = True
                    if tag == b"Z":
                        if not commit_confirmed:
                            self.errors.append("server did not confirm COMMIT")
                        else:
                            self.dropped.set()
                        self.close_connections()
                        return
                    # Hold every COMMIT response frame, including CommandComplete.
                else:
                    client.sendall(packet)
        except (EOFError, OSError):
            if not self.dropped.is_set():
                self.errors.append("backend ended before commit response loss")
