<?php
Route::get('/api/orders/{id}', [OrderController::class, 'show']);
