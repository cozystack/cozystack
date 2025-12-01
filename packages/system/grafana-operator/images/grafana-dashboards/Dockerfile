FROM alpine:3.22

RUN apk add --no-cache darkhttpd

COPY dashboards /var/www/dashboards

WORKDIR /var/www

EXPOSE 8080

CMD ["darkhttpd", "/var/www/dashboards", "--port", "8080", "--addr", "0.0.0.0"]

