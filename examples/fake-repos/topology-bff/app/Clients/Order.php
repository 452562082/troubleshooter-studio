<?php
final class OrderClient { public function create(array $body) { return Http::post('http://mall-order/internal/orders', $body); } }
