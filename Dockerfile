FROM golang:1.11

COPY build/agogos-proxy /bin/agogos-proxy

CMD ["/bin/agogos-proxy"]