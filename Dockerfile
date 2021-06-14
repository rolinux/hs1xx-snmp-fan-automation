FROM golang AS build

ENV DISTRIBUTION_DIR /go/src/git/rolinux/hs1xx-snmp-fan-automation

RUN apt-get update && apt-get install -y --no-install-recommends \
		git \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR $DISTRIBUTION_DIR
COPY . $DISTRIBUTION_DIR

RUN go mod download

RUN CGO_ENABLED=0 go build -v -a -installsuffix cgo -o hs1xx-snmp-fan-automation main.go

# run container with app on top on scratch empty container
FROM scratch

COPY --from=build /go/src/git/rolinux/hs1xx-snmp-fan-automation/hs1xx-snmp-fan-automation /bin/hs1xx-snmp-fan-automation

EXPOSE 9116

CMD ["hs1xx-snmp-fan-automation"]
