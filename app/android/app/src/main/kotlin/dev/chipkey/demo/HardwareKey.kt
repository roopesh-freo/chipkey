package dev.chipkey.demo

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyInfo
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import io.flutter.plugin.common.BinaryMessenger
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import java.security.InvalidAlgorithmParameterException
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.util.UUID
import java.util.concurrent.Executors

/**
 * The native half of the "chipkey" method channel.
 *
 * Dart can ask for three things: create a key inside secure hardware and
 * return its attestation, sign bytes with it after a biometric match, and
 * delete it. There is no method that returns the private key, because
 * Android has none either.
 */
class HardwareKey(
    private val activity: FragmentActivity,
    messenger: BinaryMessenger,
) : MethodChannel.MethodCallHandler {

    private val worker = Executors.newSingleThreadExecutor()

    init {
        MethodChannel(messenger, CHANNEL).setMethodCallHandler(this)
    }

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        when (call.method) {
            "deviceId" -> result.success(deviceId())
            // What the dashboard calls this phone, for example "Google Pixel 9".
            "deviceName" -> result.success("${Build.MANUFACTURER} ${Build.MODEL}".trim())
            "status" -> result.success(status())
            "enrol" -> enrol(call.argument<ByteArray>("challenge")!!, result)
            "sign" -> sign(
                call.argument<ByteArray>("payload")!!,
                call.argument<String>("title") ?: "Confirm with biometrics",
                call.argument<String>("subtitle") ?: "",
                result,
            )
            "delete" -> {
                keyStore().deleteEntry(ALIAS)
                result.success(null)
            }
            // Plain app settings, such as the backend address. Not secrets.
            "getSetting" -> result.success(prefs().getString(call.argument<String>("key"), null))
            "setSetting" -> {
                prefs().edit().putString(call.argument<String>("key"), call.argument<String>("value")).apply()
                result.success(null)
            }
            else -> result.notImplemented()
        }
    }

    private fun keyStore(): KeyStore = KeyStore.getInstance(KEYSTORE).apply { load(null) }

    // ---- enrol ------------------------------------------------------------

    private fun spec(challenge: ByteArray, strongBox: Boolean): KeyGenParameterSpec {
        val builder = KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN)
            .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_SHA256)
            // The chip refuses to sign without a fresh biometric match.
            .setUserAuthenticationRequired(true)
            // Enrolling a new fingerprint or face destroys the key.
            .setInvalidatedByBiometricEnrollment(true)
            // The server's nonce goes into the attestation certificate.
            .setAttestationChallenge(challenge)
            .setIsStrongBoxBacked(strongBox)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            // Timeout 0: every signature needs its own authentication.
            builder.setUserAuthenticationParameters(0, KeyProperties.AUTH_BIOMETRIC_STRONG)
        }
        return builder.build()
    }

    private fun generate(challenge: ByteArray, strongBox: Boolean) {
        KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, KEYSTORE).apply {
            initialize(spec(challenge, strongBox))
        }.generateKeyPair()
    }

    private fun enrol(challenge: ByteArray, result: MethodChannel.Result) {
        // StrongBox is slow, so key generation stays off the UI thread.
        worker.execute {
            try {
                keyStore().deleteEntry(ALIAS)
                // The ladder: ask for StrongBox, fall back to the TEE.
                var fellBack = false
                try {
                    generate(challenge, strongBox = true)
                } catch (e: StrongBoxUnavailableException) {
                    fellBack = true
                    generate(challenge, strongBox = false)
                }
                val chain = keyStore().getCertificateChain(ALIAS)?.map { it.encoded }
                    ?: throw IllegalStateException("Keystore returned no attestation chain")
                val reply = mapOf(
                    "chain" to chain,
                    "tier" to tier(),
                    "keyId" to keyId(),
                    "fellBackFromStrongBox" to fellBack,
                )
                activity.runOnUiThread { result.success(reply) }
            } catch (e: InvalidAlgorithmParameterException) {
                activity.runOnUiThread {
                    result.error("NO_BIOMETRICS", "Enrol a fingerprint or face in Settings first. ${e.message}", null)
                }
            } catch (e: Exception) {
                activity.runOnUiThread { result.error("ENROL_FAILED", e.toString(), null) }
            }
        }
    }

    // ---- sign -------------------------------------------------------------

    private fun sign(payload: ByteArray, title: String, subtitle: String, result: MethodChannel.Result) {
        val signature: Signature
        try {
            val key = keyStore().getKey(ALIAS, null) as? PrivateKey
            if (key == null) {
                result.error("NO_KEY", "No hardware key on this phone. Enrol first.", null)
                return
            }
            signature = Signature.getInstance("SHA256withECDSA").apply { initSign(key) }
        } catch (e: KeyPermanentlyInvalidatedException) {
            // A new biometric was enrolled, or the lock screen was removed.
            keyStore().deleteEntry(ALIAS)
            result.error("KEY_INVALIDATED", "The key was invalidated by a biometric or lock screen change.", null)
            return
        } catch (e: Exception) {
            result.error("SIGN_FAILED", e.toString(), null)
            return
        }

        var answered = false
        val callback = object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(auth: BiometricPrompt.AuthenticationResult) {
                if (answered) return
                answered = true
                try {
                    // The Signature object is now unlocked for exactly one use.
                    val unlocked = auth.cryptoObject?.signature
                        ?: throw IllegalStateException("Biometric result carried no signature object")
                    unlocked.update(payload)
                    result.success(unlocked.sign())
                } catch (e: Exception) {
                    result.error("SIGN_FAILED", e.toString(), null)
                }
            }

            override fun onAuthenticationError(code: Int, message: CharSequence) {
                if (answered) return
                answered = true
                val cancelled = code == BiometricPrompt.ERROR_USER_CANCELED ||
                    code == BiometricPrompt.ERROR_NEGATIVE_BUTTON ||
                    code == BiometricPrompt.ERROR_CANCELED
                result.error(if (cancelled) "CANCELLED" else "AUTH_ERROR", message.toString(), code)
            }
            // onAuthenticationFailed is a non-matching finger. The prompt stays
            // open and the user tries again, so there is nothing to report.
        }

        val prompt = BiometricPrompt(activity, ContextCompat.getMainExecutor(activity), callback)
        val info = BiometricPrompt.PromptInfo.Builder()
            .setTitle(title)
            .setSubtitle(subtitle)
            .setNegativeButtonText("Cancel")
            .setAllowedAuthenticators(BIOMETRIC_STRONG)
            .build()
        prompt.authenticate(info, BiometricPrompt.CryptoObject(signature))
    }

    // ---- status -----------------------------------------------------------

    private fun status(): Map<String, Any?> {
        val biometrics = when (BiometricManager.from(activity).canAuthenticate(BIOMETRIC_STRONG)) {
            BiometricManager.BIOMETRIC_SUCCESS -> "ready"
            BiometricManager.BIOMETRIC_ERROR_NONE_ENROLLED -> "none_enrolled"
            BiometricManager.BIOMETRIC_ERROR_NO_HARDWARE -> "no_hardware"
            else -> "unavailable"
        }
        var enrolled = false
        var tier: String? = null
        var keyId: String? = null
        try {
            enrolled = keyStore().containsAlias(ALIAS)
            if (enrolled) {
                tier = tier()
                keyId = keyId()
            }
        } catch (e: Exception) {
            tier = "unreadable"
        }
        return mapOf("enrolled" to enrolled, "tier" to tier, "keyId" to keyId, "biometrics" to biometrics)
    }

    /** Where Android says the key lives. Informational: the backend decides from the attestation. */
    private fun tier(): String {
        val key = keyStore().getKey(ALIAS, null) as PrivateKey
        val info = KeyFactory.getInstance(key.algorithm, KEYSTORE).getKeySpec(key, KeyInfo::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            return when (info.securityLevel) {
                KeyProperties.SECURITY_LEVEL_STRONGBOX -> "strongbox"
                KeyProperties.SECURITY_LEVEL_TRUSTED_ENVIRONMENT -> "tee"
                KeyProperties.SECURITY_LEVEL_SOFTWARE -> "software"
                else -> "unknown"
            }
        }
        @Suppress("DEPRECATION")
        return if (info.isInsideSecureHardware) "hardware" else "software"
    }

    /** First 8 bytes of SHA-256 over the public key's SubjectPublicKeyInfo, as hex. The backend derives the same id. */
    private fun keyId(): String {
        val spki = keyStore().getCertificate(ALIAS).publicKey.encoded
        return MessageDigest.getInstance("SHA-256").digest(spki).take(8).joinToString("") { "%02x".format(it) }
    }

    /** A random per-install id. It names the device to the backend and is not a secret. */
    private fun prefs() = activity.getSharedPreferences("chipkey", Context.MODE_PRIVATE)

    private fun deviceId(): String {
        val prefs = prefs()
        prefs.getString("deviceId", null)?.let { return it }
        val id = "d-" + UUID.randomUUID().toString().replace("-", "").take(16)
        prefs.edit().putString("deviceId", id).apply()
        return id
    }

    private companion object {
        const val CHANNEL = "chipkey"
        const val KEYSTORE = "AndroidKeyStore"
        const val ALIAS = "chipkey.device"
    }
}
