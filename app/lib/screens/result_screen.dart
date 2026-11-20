import 'package:flutter/material.dart';

import '../controller.dart';
import '../theme.dart';

/// The end of a payment: paid, blocked by the backend, or failed.
class ResultScreen extends StatelessWidget {
  const ResultScreen({
    super.key,
    required this.amount,
    required this.payee,
    required this.result,
  });
  final int amount;
  final String payee;
  final PayResult result;

  @override
  Widget build(BuildContext context) {
    final ok = result.ok;
    final colour = ok ? Brand.green : Brand.red;
    final title = ok
        ? 'Payment successful'
        : result.blocked
        ? 'Payment blocked'
        : result.sessionExpired
        ? 'Session expired'
        : 'Payment failed';
    return Scaffold(
      backgroundColor: Colors.white,
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(24, 24, 24, 20),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const Spacer(flex: 2),
              Center(
                child: TweenAnimationBuilder<double>(
                  tween: Tween(begin: 0.4, end: 1),
                  duration: const Duration(milliseconds: 450),
                  curve: Curves.elasticOut,
                  builder: (context, scale, child) =>
                      Transform.scale(scale: scale, child: child),
                  child: Container(
                    width: 104,
                    height: 104,
                    decoration: BoxDecoration(
                      color: colour,
                      shape: BoxShape.circle,
                    ),
                    child: Icon(
                      ok
                          ? Icons.check_rounded
                          : (result.blocked
                                ? Icons.gpp_bad_outlined
                                : Icons.close_rounded),
                      color: Colors.white,
                      size: 64,
                    ),
                  ),
                ),
              ),
              const SizedBox(height: 24),
              Text(
                title,
                key: const Key('resultTitle'),
                textAlign: TextAlign.center,
                style: const TextStyle(
                  color: Brand.ink,
                  fontSize: 24,
                  fontWeight: FontWeight.w700,
                ),
              ),
              const SizedBox(height: 6),
              Text(
                rupees(amount),
                textAlign: TextAlign.center,
                style: TextStyle(
                  color: Brand.ink,
                  fontSize: 40,
                  fontWeight: FontWeight.w700,
                  decoration: ok ? null : TextDecoration.lineThrough,
                  decorationColor: Brand.red,
                ),
              ),
              Text(
                ok ? 'paid to $payee' : 'to $payee was not paid',
                textAlign: TextAlign.center,
                style: const TextStyle(color: Brand.grey, fontSize: 16),
              ),
              const SizedBox(height: 22),
              Container(
                padding: const EdgeInsets.all(14),
                decoration: BoxDecoration(
                  color: ok ? Brand.greenTint : Brand.redTint,
                  borderRadius: BorderRadius.circular(12),
                ),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(
                      ok
                          ? (result.steppedUp
                                ? Icons.memory
                                : Icons.key_outlined)
                          : Icons.info_outline,
                      color: colour,
                      size: 22,
                    ),
                    const SizedBox(width: 10),
                    Expanded(
                      child: Text(
                        ok
                            ? (result.steppedUp
                                  ? 'This payment was signed inside the secure chip of this phone, after your fingerprint.'
                                  : 'A small payment, approved on your session token.')
                            : result.message,
                        key: const Key('resultDetail'),
                        style: const TextStyle(
                          color: Brand.ink,
                          fontSize: 14,
                          height: 1.45,
                        ),
                      ),
                    ),
                  ],
                ),
              ),
              if (result.txnId != null) ...[
                const SizedBox(height: 14),
                Text(
                  'Transaction ${result.txnId}',
                  textAlign: TextAlign.center,
                  style: const TextStyle(color: Brand.greyLight, fontSize: 13),
                ),
              ],
              const Spacer(flex: 3),
              BrandButton(
                key: const Key('done'),
                label: ok
                    ? 'Done'
                    : (result.sessionExpired ? 'Log in again' : 'Back'),
                background: Brand.ink,
                onPressed: () =>
                    Navigator.of(context).popUntil((route) => route.isFirst),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
